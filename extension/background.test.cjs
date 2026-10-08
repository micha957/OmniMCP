const test=require('node:test'), assert=require('node:assert/strict'), fs=require('node:fs'), vm=require('node:vm');
function harness({httpStatus=200,open=true}={}) {
 let listener, startup, alarmListener, saved={server:'http://127.0.0.1:8090',pluginId:'',accountId:'default',token:''}, urls=[];
 const timers=new Set(); const alarms=new Map(); const sockets=[]; let status=httpStatus; let checks=0;
 class WS {
  static OPEN=1; static CONNECTING=0; static CLOSED=3;
  constructor(url){sockets.push(this);urls.push(url);this.readyState=0;queueMicrotask(()=>{if(open){this.readyState=1;this.onopen?.()}else{this.onerror?.();this.readyState=3;this.onclose?.({code:1006})}})}
  close(){this.readyState=3;this.onclose?.({code:1000})} send(){}
 }
 const context={URL,WebSocket:WS,console,encodeURIComponent,Promise,Date,JSON,Number,Error,AbortSignal,
  fetch:async()=>{checks++;return {ok:status===200,status:status,json:async()=>({result:{tools:[]}})}},
  setTimeout:(f,n)=>{const h=setTimeout(f,n);h.unref?.();timers.add(h);return h},clearTimeout,
  setInterval:(f,n)=>{const h=setInterval(f,n);h.unref?.();timers.add(h);return h},clearInterval,
  chrome:{alarms:{create:async(name,info)=>{alarms.set(name,info)},onAlarm:{addListener:f=>{alarmListener=f}}},storage:{local:{get:async defaults=>({...defaults,...saved}),set:async v=>{saved={...saved,...v}}}},
   runtime:{onMessage:{addListener:f=>{listener=f}},onStartup:{addListener:f=>{startup=f}},onInstalled:{addListener:()=>{}}},tabs:{onRemoved:{addListener:()=>{}}}}};
 vm.runInNewContext(fs.readFileSync(__dirname+'/background.js','utf8'),context);
 return {urls,sockets,alarms,checks:()=>checks,startup:()=>startup(),alarm:()=>alarmListener?.({name:[...alarms.keys()][0]}),setStatus:v=>{status=v},call:msg=>new Promise(resolve=>listener(msg,{},resolve)),close:()=>{for(const h of timers){clearTimeout(h);clearInterval(h)}}};
}
test('saved MCP endpoint is normalized to the WebSocket base path and reports open',async()=>{
 const h=harness();try{await new Promise(setImmediate);
 const r=await h.call({type:'save_settings',settings:{server:'http://127.0.0.1:8090/mcp/',pluginId:'zhihu',accountId:'default',token:''}});
 assert.equal(h.urls[0],'ws://127.0.0.1:8090/extension/ws?plugin_id=zhihu&account_id=default');
 assert.equal(r.connected,true);
 }finally{h.close()}
});
test('failed handshake reports an actionable error instead of indefinite connecting',async()=>{
 const h=harness({httpStatus:401,open:false});try{await new Promise(setImmediate);
 await h.call({type:'save_settings',settings:{server:'http://127.0.0.1:8090',pluginId:'zhihu',accountId:'default',token:'fake-test-token'}});
 const r=await h.call({type:'connection_status'});
 assert.equal(r.connected,false);assert.match(r.error,/401|Token/);
 }finally{h.close()}
});

test('simultaneous wakeups create only one connection',async()=>{
 const h=harness();try{await new Promise(setImmediate);
 await h.call({type:'save_settings',settings:{pluginId:'zhihu',accountId:'default'}});
 h.sockets[0].close();
 await Promise.all([h.startup(),h.startup()]);
 assert.equal(h.urls.length,2,'one replacement socket, not competing sockets');
 assert.equal(h.checks(),2,'one preflight per connection attempt');
 }finally{h.close()}
});
test('browser alarm recovers after retry timers are lost',async()=>{
 const h=harness({httpStatus:503});try{await new Promise(setImmediate);
 await h.call({type:'save_settings',settings:{pluginId:'zhihu',accountId:'default'}});
 h.close(); // worker suspension discards ordinary timers
 h.setStatus(200);
 assert.ok(h.alarms.size,'a browser-owned wakeup must survive timer loss');
 await h.alarm();
 await new Promise(setImmediate);
 assert.equal((await h.call({type:'connection_status'})).connected,true);
 }finally{h.close()}
});
