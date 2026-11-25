//! `encoding/json`'s syntax check (`checkValid`), for its exact error messages.
//!
//! Go validates the whole input before decoding, so a syntax error is reported as
//! "invalid character 'x' looking for beginning of value" or "unexpected end of JSON input"
//! no matter what the target type is.

use crate::gofmt;

#[derive(Clone, Copy, PartialEq)]
enum Parse {
    ObjectKey,
    ObjectValue,
    ArrayValue,
}

#[derive(Clone, Copy, PartialEq)]
enum Step {
    BeginValueOrEmpty,
    BeginValue,
    BeginStringOrEmpty,
    BeginString,
    EndValue,
    EndTop,
    InString,
    InStringEsc,
    InStringEscU(u8),
    Neg,
    One,
    Zero,
    Dot,
    Dot0,
    E,
    ESign,
    E0,
    Lit(&'static [u8], usize, &'static str),
}

const MAX_DEPTH: usize = 10000;

fn quote_char(c: u8) -> String {
    match c {
        b'\'' => "'\\''".to_string(),
        b'"' => "'\"'".to_string(),
        _ => {
            let q = gofmt::quote(&char::from(c).to_string());
            format!("'{}'", &q[1..q.len() - 1])
        }
    }
}

struct Scanner {
    step: Step,
    stack: Vec<Parse>,
    end_top: bool,
}

fn is_space(c: u8) -> bool {
    matches!(c, b' ' | b'\t' | b'\r' | b'\n')
}

impl Scanner {
    fn error(c: u8, context: &str) -> Result<(), String> {
        Err(format!("invalid character {} {context}", quote_char(c)))
    }

    fn push(&mut self, c: u8, p: Parse) -> Result<(), String> {
        self.stack.push(p);
        if self.stack.len() <= MAX_DEPTH {
            Ok(())
        } else {
            Scanner::error(c, "exceeded max depth")
        }
    }

    fn pop(&mut self) {
        self.stack.pop();
        if self.stack.is_empty() {
            self.step = Step::EndTop;
            self.end_top = true;
        } else {
            self.step = Step::EndValue;
        }
    }

    fn begin_value(&mut self, c: u8) -> Result<(), String> {
        if is_space(c) {
            return Ok(());
        }
        match c {
            b'{' => {
                self.step = Step::BeginStringOrEmpty;
                self.push(c, Parse::ObjectKey)
            }
            b'[' => {
                self.step = Step::BeginValueOrEmpty;
                self.push(c, Parse::ArrayValue)
            }
            b'"' => {
                self.step = Step::InString;
                Ok(())
            }
            b'-' => {
                self.step = Step::Neg;
                Ok(())
            }
            b'0' => {
                self.step = Step::Zero;
                Ok(())
            }
            b't' => {
                self.step = Step::Lit(b"rue", 0, "true");
                Ok(())
            }
            b'f' => {
                self.step = Step::Lit(b"alse", 0, "false");
                Ok(())
            }
            b'n' => {
                self.step = Step::Lit(b"ull", 0, "null");
                Ok(())
            }
            b'1'..=b'9' => {
                self.step = Step::One;
                Ok(())
            }
            _ => Scanner::error(c, "looking for beginning of value"),
        }
    }

    fn end_value(&mut self, c: u8) -> Result<(), String> {
        let Some(&ps) = self.stack.last() else {
            self.step = Step::EndTop;
            self.end_top = true;
            return self.end_top_step(c);
        };
        if is_space(c) {
            self.step = Step::EndValue;
            return Ok(());
        }
        match ps {
            Parse::ObjectKey => {
                if c == b':' {
                    *self.stack.last_mut().unwrap_or_else(|| unreachable!()) = Parse::ObjectValue;
                    self.step = Step::BeginValue;
                    return Ok(());
                }
                Scanner::error(c, "after object key")
            }
            Parse::ObjectValue => {
                if c == b',' {
                    *self.stack.last_mut().unwrap_or_else(|| unreachable!()) = Parse::ObjectKey;
                    self.step = Step::BeginString;
                    return Ok(());
                }
                if c == b'}' {
                    self.pop();
                    return Ok(());
                }
                Scanner::error(c, "after object key:value pair")
            }
            Parse::ArrayValue => {
                if c == b',' {
                    self.step = Step::BeginValue;
                    return Ok(());
                }
                if c == b']' {
                    self.pop();
                    return Ok(());
                }
                Scanner::error(c, "after array element")
            }
        }
    }

    fn end_top_step(&mut self, c: u8) -> Result<(), String> {
        if !is_space(c) {
            return Scanner::error(c, "after top-level value");
        }
        Ok(())
    }

    fn step(&mut self, c: u8) -> Result<(), String> {
        let hex = |c: u8| c.is_ascii_hexdigit();
        match self.step {
            Step::BeginValueOrEmpty => {
                if is_space(c) {
                    return Ok(());
                }
                if c == b']' {
                    return self.end_value(c);
                }
                self.begin_value(c)
            }
            Step::BeginValue => self.begin_value(c),
            Step::BeginStringOrEmpty => {
                if is_space(c) {
                    return Ok(());
                }
                if c == b'}' {
                    *self.stack.last_mut().unwrap_or_else(|| unreachable!()) = Parse::ObjectValue;
                    return self.end_value(c);
                }
                self.begin_string(c)
            }
            Step::BeginString => self.begin_string(c),
            Step::EndValue => self.end_value(c),
            Step::EndTop => self.end_top_step(c),
            Step::InString => {
                if c == b'"' {
                    self.step = Step::EndValue;
                } else if c == b'\\' {
                    self.step = Step::InStringEsc;
                } else if c < 0x20 {
                    return Scanner::error(c, "in string literal");
                }
                Ok(())
            }
            Step::InStringEsc => match c {
                b'b' | b'f' | b'n' | b'r' | b't' | b'\\' | b'/' | b'"' => {
                    self.step = Step::InString;
                    Ok(())
                }
                b'u' => {
                    self.step = Step::InStringEscU(0);
                    Ok(())
                }
                _ => Scanner::error(c, "in string escape code"),
            },
            Step::InStringEscU(n) => {
                if !hex(c) {
                    return Scanner::error(c, "in \\u hexadecimal character escape");
                }
                self.step = if n == 3 {
                    Step::InString
                } else {
                    Step::InStringEscU(n + 1)
                };
                Ok(())
            }
            Step::Neg => match c {
                b'0' => {
                    self.step = Step::Zero;
                    Ok(())
                }
                b'1'..=b'9' => {
                    self.step = Step::One;
                    Ok(())
                }
                _ => Scanner::error(c, "in numeric literal"),
            },
            Step::One => {
                if c.is_ascii_digit() {
                    return Ok(());
                }
                self.zero(c)
            }
            Step::Zero => self.zero(c),
            Step::Dot => {
                if c.is_ascii_digit() {
                    self.step = Step::Dot0;
                    return Ok(());
                }
                Scanner::error(c, "after decimal point in numeric literal")
            }
            Step::Dot0 => {
                if c.is_ascii_digit() {
                    return Ok(());
                }
                if c == b'e' || c == b'E' {
                    self.step = Step::E;
                    return Ok(());
                }
                self.end_value(c)
            }
            Step::E => {
                if c == b'+' || c == b'-' {
                    self.step = Step::ESign;
                    return Ok(());
                }
                self.e_sign(c)
            }
            Step::ESign => self.e_sign(c),
            Step::E0 => {
                if c.is_ascii_digit() {
                    return Ok(());
                }
                self.end_value(c)
            }
            Step::Lit(rest, i, word) => {
                if c == rest[i] {
                    self.step = if i + 1 == rest.len() {
                        Step::EndValue
                    } else {
                        Step::Lit(rest, i + 1, word)
                    };
                    return Ok(());
                }
                Scanner::error(c, &format!("in literal {word} (expecting '{}')", rest[i] as char))
            }
        }
    }

    fn begin_string(&mut self, c: u8) -> Result<(), String> {
        if is_space(c) {
            return Ok(());
        }
        if c == b'"' {
            self.step = Step::InString;
            return Ok(());
        }
        Scanner::error(c, "looking for beginning of object key string")
    }

    fn zero(&mut self, c: u8) -> Result<(), String> {
        if c == b'.' {
            self.step = Step::Dot;
            return Ok(());
        }
        if c == b'e' || c == b'E' {
            self.step = Step::E;
            return Ok(());
        }
        self.end_value(c)
    }

    fn e_sign(&mut self, c: u8) -> Result<(), String> {
        if c.is_ascii_digit() {
            self.step = Step::E0;
            return Ok(());
        }
        Scanner::error(c, "in exponent of numeric literal")
    }
}

/// Go's `checkValid`: `Ok` for syntactically valid JSON, else Go's `SyntaxError` text.
pub fn check_valid(data: &[u8]) -> Result<(), String> {
    let mut s = Scanner {
        step: Step::BeginValue,
        stack: Vec::new(),
        end_top: false,
    };
    for &c in data {
        s.step(c)?;
    }
    if s.end_top {
        return Ok(());
    }
    s.step(b' ')?;
    if s.end_top {
        return Ok(());
    }
    Err("unexpected end of JSON input".to_string())
}

/// The JSON type name Go puts in an `UnmarshalTypeError` ("string", "number", "object", ...).
pub fn type_name(v: &serde_json::Value) -> &'static str {
    match v {
        serde_json::Value::Null => "null",
        serde_json::Value::Bool(_) => "bool",
        serde_json::Value::Number(_) => "number",
        serde_json::Value::String(_) => "string",
        serde_json::Value::Array(_) => "array",
        serde_json::Value::Object(_) => "object",
    }
}

#[cfg(test)]
mod tests;
