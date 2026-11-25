//! Go's `flag` package, as the Go subcommands use it: a `FlagSet` with
//! `ContinueOnError`, the same parsing rules, the same error messages and the
//! same `PrintDefaults` layout, so usage text and exit codes match byte for byte.
//!
//! Parsing never prints. An error carries the full text Go would write to stderr
//! (the message, then the usage); the caller prints it and exits 2.

use std::collections::BTreeMap;

use crate::gofmt::quote;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Kind {
    Str,
    Bool,
    Int,
    /// A repeatable string (`fs.Var` with an appending value).
    List,
}

#[derive(Clone, Debug)]
struct Flag {
    kind: Kind,
    usage: String,
    default: String,
}

/// A parsed flag value.
#[derive(Clone, Debug, PartialEq, Eq)]
enum Value {
    Str(String),
    Bool(bool),
    Int(i64),
    List(Vec<String>),
}

/// One subcommand's flags, declared like Go's `flag.NewFlagSet(name, flag.ContinueOnError)`.
#[derive(Clone, Debug)]
pub struct FlagSet {
    name: String,
    flags: BTreeMap<String, Flag>,
    /// Text a custom `fs.Usage` prints before `PrintDefaults`; `None` is Go's default usage.
    usage_header: Option<String>,
}

/// Why parsing stopped.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ParseError {
    /// `-h` or `-help` (when not defined): the usage text, Go's `flag.ErrHelp`.
    Help(String),
    /// A bad flag: the message and the usage, exactly as Go writes them to stderr.
    Bad(String),
}

impl ParseError {
    /// The text Go writes to stderr for this error.
    pub fn stderr(&self) -> &str {
        match self {
            ParseError::Help(s) | ParseError::Bad(s) => s,
        }
    }
}

/// The flag values after a successful parse, and the remaining positional arguments.
#[derive(Clone, Debug)]
pub struct Parsed {
    values: BTreeMap<String, Value>,
    /// Arguments after the flags (Go's `fs.Args()`).
    pub rest: Vec<String>,
}

impl Parsed {
    /// A string flag's value. Panics only on a programming error (undeclared or not a string).
    pub fn str(&self, name: &str) -> &str {
        match self.values.get(name) {
            Some(Value::Str(s)) => s,
            _ => panic!("flag -{name} is not a declared string flag"),
        }
    }

    /// A bool flag's value.
    pub fn bool(&self, name: &str) -> bool {
        match self.values.get(name) {
            Some(Value::Bool(b)) => *b,
            _ => panic!("flag -{name} is not a declared bool flag"),
        }
    }

    /// A list flag's values, in the order given.
    pub fn list(&self, name: &str) -> &[String] {
        match self.values.get(name) {
            Some(Value::List(v)) => v,
            _ => panic!("flag -{name} is not a declared list flag"),
        }
    }

    /// An int flag's value.
    pub fn int(&self, name: &str) -> i64 {
        match self.values.get(name) {
            Some(Value::Int(i)) => *i,
            _ => panic!("flag -{name} is not a declared int flag"),
        }
    }
}

impl FlagSet {
    /// `flag.NewFlagSet(name, flag.ContinueOnError)`.
    pub fn new(name: &str) -> Self {
        Self {
            name: name.to_string(),
            flags: BTreeMap::new(),
            usage_header: None,
        }
    }

    /// `fs.String(name, default, usage)`.
    pub fn string(&mut self, name: &str, default: &str, usage: &str) -> &mut Self {
        self.add(name, Kind::Str, default, usage)
    }

    /// `fs.Bool(name, default, usage)`.
    pub fn bool(&mut self, name: &str, default: bool, usage: &str) -> &mut Self {
        self.add(
            name,
            Kind::Bool,
            if default { "true" } else { "false" },
            usage,
        )
    }

    /// `fs.Int(name, default, usage)`.
    pub fn int(&mut self, name: &str, default: i64, usage: &str) -> &mut Self {
        self.add(name, Kind::Int, &default.to_string(), usage)
    }

    /// `fs.Var(&repeated, name, usage)`: a repeatable string flag.
    pub fn list(&mut self, name: &str, usage: &str) -> &mut Self {
        self.add(name, Kind::List, "[]", usage)
    }

    /// A custom `fs.Usage` that prints `header` and then `fs.PrintDefaults()`.
    pub fn usage_header(&mut self, header: &str) -> &mut Self {
        self.usage_header = Some(header.to_string());
        self
    }

    fn add(&mut self, name: &str, kind: Kind, default: &str, usage: &str) -> &mut Self {
        self.flags.insert(
            name.to_string(),
            Flag {
                kind,
                usage: usage.to_string(),
                default: default.to_string(),
            },
        );
        self
    }

    /// The usage text Go prints for this set (`fs.Usage()`).
    pub fn usage(&self) -> String {
        let mut out = match &self.usage_header {
            Some(h) => h.clone(),
            None if self.name.is_empty() => "Usage:\n".to_string(),
            None => format!("Usage of {}:\n", self.name),
        };
        out.push_str(&self.print_defaults());
        out
    }

    /// `fs.PrintDefaults()`.
    pub fn print_defaults(&self) -> String {
        let mut out = String::new();
        for (name, flag) in &self.flags {
            let mut b = format!("  -{name}");
            let (arg, usage) = unquote_usage(flag);
            if !arg.is_empty() {
                b.push(' ');
                b.push_str(&arg);
            }
            if b.len() <= 4 {
                b.push('\t');
            } else {
                b.push_str("\n    \t");
            }
            b.push_str(&usage.replace('\n', "\n    \t"));
            if !is_zero(flag) {
                if flag.kind == Kind::Str {
                    b.push_str(&format!(" (default {})", quote(&flag.default)));
                } else {
                    b.push_str(&format!(" (default {})", flag.default));
                }
            }
            out.push_str(&b);
            out.push('\n');
        }
        out
    }

    /// `fs.Parse(args)`.
    pub fn parse(&self, args: &[String]) -> Result<Parsed, ParseError> {
        let mut values: BTreeMap<String, Value> = self
            .flags
            .iter()
            .map(|(n, f)| {
                let v = match f.kind {
                    Kind::Str => Value::Str(f.default.clone()),
                    Kind::Bool => Value::Bool(f.default == "true"),
                    Kind::Int => Value::Int(f.default.parse().unwrap_or(0)),
                    Kind::List => Value::List(Vec::new()),
                };
                (n.clone(), v)
            })
            .collect();
        let mut args: &[String] = args;
        while let Some(s) = args.first() {
            let b = s.as_bytes();
            if b.len() < 2 || b[0] != b'-' {
                break;
            }
            let mut minuses = 1;
            if b[1] == b'-' {
                minuses = 2;
                if b.len() == 2 {
                    args = &args[1..];
                    break;
                }
            }
            let mut name = &s[minuses..];
            if name.is_empty() || name.starts_with('-') || name.starts_with('=') {
                return Err(self.fail(&format!("bad flag syntax: {s}")));
            }
            args = &args[1..];
            let mut value: Option<&str> = None;
            if let Some(i) = name[1..].find('=') {
                value = Some(&name[i + 2..]);
                name = &name[..i + 1];
            }
            let Some(flag) = self.flags.get(name) else {
                if name == "help" || name == "h" {
                    return Err(ParseError::Help(self.usage()));
                }
                return Err(self.fail(&format!("flag provided but not defined: -{name}")));
            };
            match flag.kind {
                Kind::Bool => {
                    let v = value.unwrap_or("true");
                    match parse_bool(v) {
                        Some(b) => {
                            values.insert(name.to_string(), Value::Bool(b));
                        }
                        None => {
                            return Err(self.fail(&format!(
                                "invalid boolean value {} for -{name}: parse error",
                                quote(v)
                            )));
                        }
                    }
                }
                Kind::Str | Kind::Int | Kind::List => {
                    let v = match value {
                        Some(v) => v.to_string(),
                        None => match args.first() {
                            Some(next) => {
                                let next = next.clone();
                                args = &args[1..];
                                next
                            }
                            None => {
                                return Err(self.fail(&format!("flag needs an argument: -{name}")));
                            }
                        },
                    };
                    if flag.kind == Kind::Str {
                        values.insert(name.to_string(), Value::Str(v));
                    } else if flag.kind == Kind::List {
                        if let Some(Value::List(l)) = values.get_mut(name) {
                            l.push(v);
                        }
                    } else {
                        match parse_int(&v) {
                            Ok(i) => {
                                values.insert(name.to_string(), Value::Int(i));
                            }
                            Err(e) => {
                                return Err(self.fail(&format!(
                                    "invalid value {} for flag -{name}: {e}",
                                    quote(&v)
                                )));
                            }
                        }
                    }
                }
            }
        }
        Ok(Parsed {
            values,
            rest: args.to_vec(),
        })
    }

    fn fail(&self, msg: &str) -> ParseError {
        ParseError::Bad(format!("{msg}\n{}", self.usage()))
    }
}

/// Go's `flag.UnquoteUsage`: a back-quoted word in the usage names the argument;
/// otherwise the type does (`string`, `int`; nothing for a bool).
fn unquote_usage(flag: &Flag) -> (String, String) {
    let u = &flag.usage;
    if let Some(start) = u.find('`')
        && let Some(len) = u[start + 1..].find('`')
    {
        let name = &u[start + 1..start + 1 + len];
        let usage = format!("{}{}{}", &u[..start], name, &u[start + 1 + len + 1..]);
        return (name.to_string(), usage);
    }
    let name = match flag.kind {
        Kind::Bool => "",
        Kind::Int => "int",
        Kind::List => "value",
        Kind::Str => "string",
    };
    (name.to_string(), u.clone())
}

fn is_zero(flag: &Flag) -> bool {
    match flag.kind {
        Kind::Str => flag.default.is_empty(),
        Kind::Bool => flag.default == "false",
        Kind::Int => flag.default == "0",
        Kind::List => true,
    }
}

/// `strconv.ParseBool`.
pub(crate) fn parse_bool(s: &str) -> Option<bool> {
    match s {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Some(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Some(false),
        _ => None,
    }
}

fn underscore_ok(s: &str) -> bool {
    let s = s.strip_prefix(['+', '-']).unwrap_or(s).as_bytes();
    let mut saw = b'^';
    let mut i = 0;
    let mut hex = false;
    if s.len() >= 2 && s[0] == b'0' && matches!(s[1].to_ascii_lowercase(), b'b' | b'o' | b'x') {
        i = 2;
        saw = b'0';
        hex = s[1].eq_ignore_ascii_case(&b'x');
    }
    while i < s.len() {
        let c = s[i];
        if c.is_ascii_digit() || (hex && c.to_ascii_lowercase().is_ascii_hexdigit()) {
            saw = b'1';
        } else if c == b'_' {
            if saw != b'1' {
                return false;
            }
            saw = b'_';
        } else {
            if saw == b'_' {
                return false;
            }
            saw = b'!';
        }
        i += 1;
    }
    saw != b'_'
}

/// `strconv.ParseInt(s, 0, 64)`; the error is Go's flag wording ("parse error" or "value out of
/// range").
pub fn parse_int(s0: &str) -> Result<i64, &'static str> {
    const SYNTAX: &str = "parse error";
    const RANGE: &str = "value out of range";
    let (neg, s) = match s0.as_bytes().first() {
        Some(b'+') => (false, &s0[1..]),
        Some(b'-') => (true, &s0[1..]),
        _ => (false, s0),
    };
    if s.is_empty() {
        return Err(SYNTAX);
    }
    let b = s.as_bytes();
    let (base, digits) = if b[0] == b'0' {
        if b.len() >= 3 && b[1].eq_ignore_ascii_case(&b'b') {
            (2, &s[2..])
        } else if b.len() >= 3 && b[1].eq_ignore_ascii_case(&b'o') {
            (8, &s[2..])
        } else if b.len() >= 3 && b[1].eq_ignore_ascii_case(&b'x') {
            (16, &s[2..])
        } else {
            (8, &s[1..])
        }
    } else {
        (10, s)
    };
    let mut n: u64 = 0;
    let mut underscores = false;
    for c in digits.chars() {
        if c == '_' {
            underscores = true;
            continue;
        }
        let d = c.to_digit(base).ok_or(SYNTAX)?;
        n = n
            .checked_mul(u64::from(base))
            .and_then(|n| n.checked_add(u64::from(d)))
            .ok_or(RANGE)?;
    }
    if underscores && !underscore_ok(s0) {
        return Err(SYNTAX);
    }
    if neg {
        if n > 1 << 63 {
            return Err(RANGE);
        }
        Ok((n as i64).wrapping_neg())
    } else {
        i64::try_from(n).map_err(|_overflow| RANGE)
    }
}

#[cfg(test)]
mod tests;
