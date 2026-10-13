import assert from 'node:assert/strict';
import test from 'node:test';

test('Reload restores the tab session and does not close it on pagehide',async()=>{
  const values=new Map(),requests=[],events=[];
  globalThis.sessionStorage={getItem:key=>values.get(key),setItem:(key,value)=>values.set(key,value)};
  globalThis.document={querySelector:()=>({content:'viewer-token'})};
  globalThis.window={addEventListener:(name,callback)=>events.push({name,callback})};
  globalThis.fetch=async(path,options)=>{
    requests.push({path,options});
    return {ok:true,json:async()=>({token:'viewer-token',session:'existing-session',settings:{stream_bitrate:10000}})};
  };
  const first=await import('./api.js?first-load');
  assert.equal((await first.bootstrap()).stream_bitrate,10000);
  assert.equal(values.get('anyviewer-session'),'existing-session');
  const reloaded=await import('./api.js?reload');
  await reloaded.bootstrap();
  assert.equal(requests[1].options.headers['X-Viewer-Session'],'existing-session');
  assert.equal(requests[1].options.headers['X-Viewer-Token'],'viewer-token');
  assert.equal(events.filter(event=>event.name==='pagehide').length,0);
});


test('Expired sessions recover to a fresh disconnected session without reusing the old one',async()=>{
  const requests=[];
  globalThis.fetch=async(path,options)=>{
    requests.push({path,options});
    if(path==='/bootstrap') return {ok:true,status:200,json:async()=>({token:'viewer-token',session:requests.length>2?'fresh-session':'expired-session',settings:{}})};
    if(options.headers['X-Viewer-Session']==='expired-session') return {ok:false,status:410};
    return {ok:true,status:200,json:async()=>({state:'disconnected'})};
  };
  const api=await import('./api.js?expired-session');
  const response=await api.request('/status');
  assert.equal(response.status,200);
  assert.equal(requests.at(-1).options.headers['X-Viewer-Session'],'fresh-session');
});
