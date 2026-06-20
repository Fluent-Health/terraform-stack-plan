// Pure terminal-output helpers for the live viewer's Log tab: ANSI SGR → HTML
// and a carriage-return-aware line buffer. Dual-mode: attaches to the browser
// global AND exports for the node unit test. All log bytes are HTML-escaped
// before insertion (logs are attacker-influenceable).
(function (root) {
  var MAP = {31:'a-red',32:'a-green',33:'a-yellow',34:'a-blue',35:'a-magenta',36:'a-cyan',90:'a-grey',
             91:'a-red',92:'a-green',93:'a-yellow',94:'a-blue',95:'a-magenta',96:'a-cyan'};
  function esc(s){ return s.replace(/[&<>]/g, function (c){ return {'&':'&amp;','<':'&lt;','>':'&gt;'}[c]; }); }
  function cls(codes){ var c=[]; codes.split(';').forEach(function(n){ n=+n; if(n===1)c.push('a-bold'); else if(MAP[n])c.push(MAP[n]); }); return c.join(' '); }
  function ansi(line){
    var out='', open=0, re=/\x1b\[([0-9;]*)m/g, last=0, m;
    while ((m=re.exec(line))){
      out+=esc(line.slice(last,m.index)); last=re.lastIndex;
      if (open){ out+='</span>'; open=0; }
      var codes=m[1]||'0';
      if (codes!=='0' && codes!==''){ var c=cls(codes); if(c){ out+='<span class="'+c+'">'; open=1; } }
    }
    out+=esc(line.slice(last)); if (open) out+='</span>';
    return out;
  }
  // LineBuffer interprets \n (flush completed line) and \r (overwrite current
  // line) across streamed chunks. push() returns the newly completed lines and
  // the current pending line, both ANSI-rendered to HTML.
  function LineBuffer(){ this.pending=''; }
  LineBuffer.prototype.push = function(chunk){
    var completed=[];
    for (var i=0;i<chunk.length;i++){
      var ch=chunk[i];
      if (ch==='\n'){ completed.push(ansi(this.pending)); this.pending=''; }
      else if (ch==='\r'){ this.pending=''; }
      else { this.pending+=ch; }
    }
    return { completed: completed, pending: ansi(this.pending) };
  };
  // renderStatic collapses \r-runs per line (final frame wins) for a non-live
  // fetch of a finished log.
  function renderStatic(text){
    return text.split('\n').map(function(line){ var s=line.split('\r'); return ansi(s[s.length-1]); });
  }
  var api = { ansi: ansi, LineBuffer: LineBuffer, renderStatic: renderStatic };
  root.tfspTerm = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof window !== 'undefined' ? window : (typeof globalThis !== 'undefined' ? globalThis : this));
