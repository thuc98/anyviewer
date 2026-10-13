import { errorMessage } from './errors.js';
let token = document.querySelector('meta[name="viewer-token"]')?.content || '';
const sessionKey = 'anyviewer-session';
let session = '';
try { session=sessionStorage.getItem(sessionKey) || ''; if(!token || token==='__TOKEN__') token=sessionStorage.getItem('anyviewer-token') || ''; } catch {}
let initialization;
let recovery;

async function responseData(response) {
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {const error=new Error(data.error ? errorMessage(data.error) : 'Không thể thực hiện yêu cầu. Vui lòng thử lại.');error.diagnostic=data.error || `HTTP ${response.status}`;throw error;}
  return data;
}
export function bootstrap() {
  if (!initialization) initialization=(async()=>{
    const headers=session ? {'X-Viewer-Token':token,'X-Viewer-Session':session} : {};
    let response=await fetch('/bootstrap',{cache:'no-store',headers});
    if(response.status===403 && session) {session='';response=await fetch('/bootstrap',{cache:'no-store'});}
    const data=await responseData(response);
    token=data.token;session=data.session;
    try {sessionStorage.setItem(sessionKey,session);sessionStorage.setItem('anyviewer-token',token);} catch {}
    // Reload/navigation keeps the controller alive. Missing status heartbeats
    // expire its session after one minute on the backend.

    return data.settings;
  })();
  return initialization;
}
export async function request(path, options={}) {
  await bootstrap();
  const usedSession=session;
  const send=()=>fetch(path,{...options,headers:{...options.headers,'X-Viewer-Token':token,'X-Viewer-Session':session}});
  let response=await send();
  if(response.status===410) {
    if(usedSession===session) {
      if(!recovery) recovery=(async()=>{initialization=undefined;await bootstrap();})().finally(()=>{recovery=undefined;});
      await recovery;
    }
    response=await send();
  }
  return response;
}
export async function post(path, value = {}) {
  return responseData(await request(path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(value)}));
}
export async function upload(file, signal) {
  const body=new FormData();body.append('file',file);
  return responseData(await request('/file',{method:'POST',body,signal}));
}
export async function getClipboard(signal) {
  return responseData(await request('/clipboard',{cache:'no-store',signal}));
}
export async function downloadFile(id,name) {
  const response=await request(`/file/download?id=${encodeURIComponent(id)}`);
  if(!response.ok) return responseData(response);
  const url=URL.createObjectURL(await response.blob());
  const link=document.createElement('a');link.href=url;link.download=name;
  document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1000);
}

export async function streamSocket(streamID, path = '/stream/ws') {
  await bootstrap();
  const url=new URL(path,window.location.href);
  if(streamID) url.searchParams.set('stream_id',streamID);
  url.protocol=location.protocol==='https:' ? 'wss:' : 'ws:';
  return new WebSocket(url,['viewer',token,session]);
}
