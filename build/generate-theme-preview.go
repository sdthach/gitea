// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build ignore

// Renders theme-preview/index.html, a standalone page that switches between
// every sheet in web_src/css/themes and swatches the variables each resolves.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	themeDir  = "web_src/css/themes"
	outDir    = "theme-preview"
	reference = "theme-gitea-dark.css"
)

var varDecl = regexp.MustCompile(`(?m)^\s*(--[\w-]+)\s*:`)

const page = `<!doctype html>
<meta charset="utf-8">
<title>Gitea theme preview</title>
<link id="theme" rel="stylesheet" href="../web_src/css/themes/THEME0">
<style>
* { box-sizing: border-box; }
body { margin: 0; display: flex; height: 100vh; font: 14px/1.5 system-ui, sans-serif;
       background: var(--color-body); color: var(--color-text); }
aside { width: 260px; flex: none; overflow-y: auto; padding: 12px;
        background: var(--color-nav-bg, var(--color-secondary-bg)); border-right: 1px solid var(--color-secondary); }
aside a { display: block; padding: 4px 6px; border-radius: 4px; text-decoration: none;
          color: var(--color-text); font-size: 12px; cursor: pointer; }
aside a:hover { background: var(--color-hover); }
aside a.on { background: var(--color-primary); color: var(--color-primary-contrast); }
#modes { display: flex; margin-bottom: 8px; }
#modes button { flex: 1; padding: 5px 0; font-size: 12px; border: 1px solid var(--color-secondary);
                background: var(--color-secondary-button); color: var(--color-text); border-radius: 0; }
#modes button:first-child { border-radius: 4px 0 0 4px; }
#modes button:last-child { border-radius: 0 4px 4px 0; }
#modes button.on { background: var(--color-primary); color: var(--color-primary-contrast); border-color: var(--color-primary); }
#filter { width: 100%; padding: 5px 7px; margin-bottom: 8px; font: inherit; font-size: 12px; border-radius: 4px;
          border: 1px solid var(--color-secondary); background: var(--color-input-background); color: var(--color-text); }
#count { font-size: 11px; color: var(--color-text-light-2); margin-bottom: 6px; }
main { flex: 1; overflow-y: auto; padding: 24px; }
h1 { margin: 0 0 4px; font-size: 20px; }
h2 { font-size: 13px; text-transform: uppercase; letter-spacing: .05em;
     color: var(--color-text-light-2); margin: 28px 0 10px; }
.row { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
button { font: inherit; padding: 7px 14px; border-radius: 4px; border: 1px solid transparent; cursor: pointer; }
.btn-primary { background: var(--color-primary); color: var(--color-primary-contrast); }
.btn-secondary { background: var(--color-secondary-button); color: var(--color-text); border-color: var(--color-secondary); }
.btn-green { background: var(--color-green); color: var(--color-white); }
.btn-red { background: var(--color-red); color: var(--color-white); }
.btn-orange { background: var(--color-orange); color: var(--color-white); }
.msg { padding: 10px 14px; border-radius: 4px; border: 1px solid; margin-bottom: 8px; }
.msg.success { background: var(--color-success-bg); border-color: var(--color-success-border); color: var(--color-success-text); }
.msg.info { background: var(--color-info-bg); border-color: var(--color-info-border); color: var(--color-info-text); }
.msg.warning { background: var(--color-warning-bg); border-color: var(--color-warning-border); color: var(--color-warning-text); }
.msg.error { background: var(--color-error-bg); border-color: var(--color-error-border); color: var(--color-error-text); }
.card { background: var(--color-box-body); border: 1px solid var(--color-secondary); border-radius: 6px; overflow: hidden; }
.card-head { background: var(--color-box-header); padding: 10px 14px; border-bottom: 1px solid var(--color-secondary); }
.card-body { padding: 14px; }
a.link { color: var(--color-primary); }
pre { background: var(--color-code-bg); color: var(--color-text); padding: 12px; border-radius: 6px;
      overflow-x: auto; font: 12px/1.6 ui-monospace, monospace; }
.k { color: var(--color-syntax-keyword); } .s { color: var(--color-syntax-string); }
.c { color: var(--color-syntax-comment); } .f { color: var(--color-syntax-function); }
.v { color: var(--color-syntax-variable); } .n { color: var(--color-syntax-constant); }
.diff { border: 1px solid var(--color-secondary); border-radius: 6px; overflow: hidden; }
.diff div { display: flex; font: 12px/1.7 ui-monospace, monospace; }
.diff u { flex: none; width: 46px; text-align: right; padding-right: 8px; text-decoration: none;
          color: var(--color-text-light-3); }
.diff p { margin: 0; padding: 0 8px; flex: 1; }
.diff i { font-style: normal; }
.diff .add { background: var(--color-diff-added-row-bg); border-left: 3px solid var(--color-diff-added-row-border); }
.diff .add u { background: var(--color-diff-added-linenum-bg); }
.diff .add p { color: var(--color-diff-added-fg); }
.diff .add i { background: var(--color-diff-added-word-bg); }
.diff .del { background: var(--color-diff-removed-row-bg); border-left: 3px solid var(--color-diff-removed-row-border); }
.diff .del u { background: var(--color-diff-removed-linenum-bg); }
.diff .del p { color: var(--color-diff-removed-fg); }
.diff .del i { background: var(--color-diff-removed-word-bg); }
.diff .ctx { border-left: 3px solid transparent; }
.diff .ctx p { color: var(--color-text-light-2); }
.diff .mov { background: var(--color-diff-moved-row-bg); border-left: 3px solid var(--color-diff-moved-row-border); }
.labels span { padding: 2px 8px; border-radius: 10px; font-size: 12px; }
.swatches { display: grid; grid-template-columns: repeat(auto-fill, minmax(190px, 1fr)); gap: 4px; }
.sw { display: flex; align-items: center; gap: 6px; font: 10px/1.2 ui-monospace, monospace; }
.sw b { width: 22px; height: 22px; flex: none; border-radius: 3px; border: 1px solid var(--color-secondary);
        background-image: linear-gradient(45deg,#8884 25%,#0000 25%,#0000 75%,#8884 75%), linear-gradient(45deg,#8884 25%,#0000 25%,#0000 75%,#8884 75%);
        background-size: 8px 8px; background-position: 0 0, 4px 4px; }
.sw b i { display: block; width: 100%; height: 100%; }
.sw span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sw em { color: var(--color-text-light-2); font-style: normal; }
</style>
<aside>
  <div id="modes"></div>
  <input id="filter" placeholder="filter themes…" autocomplete="off">
  <div id="count"></div>
  <div id="list"></div>
</aside>
<main>
  <h1 id="name"></h1>
  <div id="file" style="color: var(--color-text-light-2); font-size: 12px"></div>

  <h2>Buttons</h2>
  <div class="row">
    <button class="btn-primary">New Pull Request</button>
    <button class="btn-secondary">Cancel</button>
    <button class="btn-green">Merge</button>
    <button class="btn-red">Delete</button>
    <button class="btn-orange">Reopen</button>
    <a class="link" href="#">a link</a>
  </div>

  <h2>Banners</h2>
  <div class="msg success">Branch merged successfully.</div>
  <div class="msg info">This repository is a mirror.</div>
  <div class="msg warning">This pull request has conflicts.</div>
  <div class="msg error">Something went wrong.</div>

  <h2>Box</h2>
  <div class="card">
    <div class="card-head"><b>README.md</b></div>
    <div class="card-body">Body text on <code>--color-box-body</code>, with
      <span style="color: var(--color-text-light-2)">muted secondary text</span>.</div>
  </div>

  <h2>Labels</h2>
  <div class="row labels">
    <span style="background: var(--color-red); color: var(--color-white)">bug</span>
    <span style="background: var(--color-green); color: var(--color-white)">enhancement</span>
    <span style="background: var(--color-blue); color: var(--color-white)">docs</span>
    <span style="background: var(--color-purple); color: var(--color-white)">question</span>
    <span style="background: var(--color-yellow); color: var(--color-black)">help wanted</span>
    <span style="background: var(--color-teal); color: var(--color-white)">ci</span>
  </div>

  <h2>Code</h2>
  <pre><span class="c">// merge the pull request</span>
<span class="k">func</span> <span class="f">Merge</span>(pr *<span class="n">PullRequest</span>) <span class="k">error</span> {
  <span class="v">msg</span> := <span class="s">"merged via preview"</span>
  <span class="k">return</span> <span class="f">doMerge</span>(<span class="v">pr</span>, <span class="v">msg</span>)
}</pre>

  <h2>Diff</h2>
  <div class="diff">
    <div class="ctx"><u>41</u><p>&nbsp; if err != nil {</p></div>
    <div class="del"><u>42</u><p>-&nbsp; return <i>oldValue</i></p></div>
    <div class="add"><u>42</u><p>+&nbsp; return <i>newValue</i></p></div>
    <div class="mov"><u>43</u><p>&nbsp; // moved from another file</p></div>
    <div class="ctx"><u>44</u><p>&nbsp; }</p></div>
  </div>

  <h2>All variables</h2>
  <div class="swatches" id="sw"></div>
</main>
<script>
const THEMES = __THEMES__;
const VARS = __VARS__;
const MODES = ['light', 'dark', 'auto'];
const link = document.getElementById('theme');
const list = document.getElementById('list');

// theme-dracula-pro-abyss-light -> {family: 'dracula pro abyss', mode: 'light'}
// theme-gitea-dark-tritanopia   -> {family: 'gitea tritanopia',  mode: 'dark'}
const families = new Map();
for (const f of THEMES) {
  const parts = f.replace(/^theme-/, '').replace(/\.css$/, '').split('-');
  const i = parts.findIndex((p) => MODES.includes(p));
  const mode = parts[i];
  const family = parts.filter((_, j) => j !== i).join(' ');
  if (!families.has(family)) families.set(family, {});
  families.get(family)[mode] = f;
}

let mode = 'dark';
let family = [...families.keys()].find((k) => k.startsWith('dracula')) || [...families.keys()][0];

function render() {
  const cs = getComputedStyle(document.documentElement);
  const sw = document.getElementById('sw');
  sw.innerHTML = '';
  for (const v of VARS) {
    const val = cs.getPropertyValue(v).trim();
    const d = document.createElement('div');
    d.className = 'sw';
    d.innerHTML = '<b><i style="background:' + val + '"></i></b><span>' + v.slice(2) +
      '<br><em>' + val + '</em></span>';
    sw.appendChild(d);
  }
}

function apply() {
  const f = families.get(family)[mode] || Object.values(families.get(family))[0];
  link.href = '../web_src/css/themes/' + f;
  document.getElementById('name').textContent = family;
  document.getElementById('file').textContent = f;
  for (const a of list.children) a.classList.toggle('on', a.dataset.k === family);
  for (const b of document.getElementById('modes').children) b.classList.toggle('on', b.dataset.m === mode);
  location.hash = family + '/' + mode;
  setTimeout(render, 60);
}

function buildList(q) {
  list.innerHTML = '';
  let n = 0;
  for (const k of families.keys()) {
    if (q && !k.includes(q)) continue;
    n++;
    const a = document.createElement('a');
    a.textContent = k;
    a.dataset.k = k;
    a.classList.toggle('on', k === family);
    a.onclick = () => { family = k; apply(); };
    list.appendChild(a);
  }
  document.getElementById('count').textContent = n + ' of ' + families.size + ' themes';
}

for (const m of MODES) {
  const b = document.createElement('button');
  b.textContent = m;
  b.dataset.m = m;
  b.onclick = () => { mode = m; apply(); };
  document.getElementById('modes').appendChild(b);
}
document.getElementById('filter').oninput = (e) => buildList(e.target.value.trim().toLowerCase());

const [hf, hm] = decodeURIComponent(location.hash.slice(1)).split('/');
if (families.has(hf)) family = hf;
if (MODES.includes(hm)) mode = hm;
buildList('');
apply();
link.addEventListener('load', render);
</script>
`

func jsArray(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func main() {
	sheets, err := filepath.Glob(filepath.Join(themeDir, "theme-*.css"))
	if err != nil {
		panic(err)
	}
	if len(sheets) == 0 {
		panic("no theme stylesheet found in " + themeDir)
	}
	themes := make([]string, len(sheets))
	for i, sheet := range sheets {
		themes[i] = filepath.Base(sheet)
	}
	slices.Sort(themes)

	ref, err := os.ReadFile(filepath.Join(themeDir, reference))
	if err != nil {
		panic(err)
	}
	var ordered []string
	seen := make(map[string]bool)
	for _, match := range varDecl.FindAllStringSubmatch(string(ref), -1) {
		name := match[1]
		if !seen[name] && !strings.HasPrefix(name, "--theme-") {
			seen[name] = true
			ordered = append(ordered, name)
		}
	}

	html := strings.ReplaceAll(page, "THEME0", themes[0])
	html = strings.ReplaceAll(html, "__THEMES__", jsArray(themes))
	html = strings.ReplaceAll(html, "__VARS__", jsArray(ordered))

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}
	dst := filepath.Join(outDir, "index.html")
	if err := os.WriteFile(dst, []byte(html), 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("%d themes, %d vars -> %s\n", len(themes), len(ordered), dst)
}
