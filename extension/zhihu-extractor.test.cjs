const test = require('node:test');
const assert = require('node:assert/strict');
const extract = require('./zhihu-extractor.js');
const node = (text, fields={}) => ({innerText:text,textContent:text,querySelector:()=>null,querySelectorAll:()=>[],...fields});
test('article extraction preserves long body and paragraph anchors, excluding page chrome',()=>{
 const body=node('甲'.repeat(25001),{innerHTML:'<p>original</p>',children:[node('甲',{tagName:'P'}),node('结尾',{tagName:'P'})]});
 const nodes={'.Post-RichText':body,'h1.Post-Title':node('title'),'.ContentItem-time':node('发布于 2023-01-01')};
 const doc={body:node('导航 推荐 正文'),querySelector:s=>nodes[s]||null,querySelectorAll:()=>[]};
 const result=extract(doc,'https://zhuanlan.zhihu.com/p/123','article');
 assert.equal(result.body.length,25001);assert.equal(result.status,'ok');assert.equal(result.blocks[1].index,2);assert.equal(result.completeness.last_block,'结尾');
});
test('missing or restricted body is never a successful full article',()=>{
 const doc={body:node('请登录后查看'),querySelector:()=>null,querySelectorAll:()=>[]};
 assert.equal(extract(doc,'https://www.zhihu.com/people/a/posts','author').status,'login_required');
 assert.equal(extract(doc,'https://zhuanlan.zhihu.com/p/1','article').status,'not_ready');
 doc.querySelector=s=>s==='.Post-RichText'?node('正文',{children:[]}):s==='h1.Post-Title'?node('标题'):null;
 doc.body=node('付费后阅读全文');
 assert.equal(extract(doc,'https://zhuanlan.zhihu.com/p/1','article').status,'partial');
});
