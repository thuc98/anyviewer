let token = document.querySelector('meta[name="viewer-token"]')?.content || '';
let session = '';
let initialization;

async function responseData(response) {
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || 'Không thể thực hiện yêu cầu. Vui lòng thử lại.');
  return data;
}
export function bootstrap() {
  if (!initialization) initialization=(async()=>{
    const data=await responseData(await fetch('/bootstrap',{cache:'no-store'}));
    token=data.token;session=data.session;
    window.addEventListener('pagehide',event=>{
      if(event.persisted || !session) return;
      fetch('/session/close',{method:'POST',headers:{'Content-Type':'application/json','X-Viewer-Token':token,'X-Viewer-Session':session},body:'{}',keepalive:true}).catch(()=>{});
    });
    return data.settings;
  })();
  return initialization;
}
export async function request(path, options={}) {
  await bootstrap();
  return fetch(path,{...options,headers:{...options.headers,'X-Viewer-Token':token,'X-Viewer-Session':session}});
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
