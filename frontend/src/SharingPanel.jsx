import {useEffect,useRef,useState} from 'react';
import {post,request,streamSocket} from './api';
import {startBrowserShare} from './sharing';
import {errorMessage} from './errors';

export default function Sharing({onClose}) {
  const [status,setStatus]=useState({state:'stopped'});
  const [active,setActive]=useState(false);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState('');
  const [message,setMessage]=useState(''),[chatBusy,setChatBusy]=useState(false),[chatError,setChatError]=useState('');
  const [resolution,setResolution]=useState('1920x1080'),[quality,setQuality]=useState(92),[fps,setFPS]=useState(30);
  const source=useRef(null),sender=useRef(null),preview=useRef(null),generation=useRef(0),shareID=useRef(null),chatList=useRef(null);
  useEffect(()=>{if(chatList.current) chatList.current.scrollTop=chatList.current.scrollHeight;},[status.messages?.length]);
  useEffect(()=>{
    let closed=false,controller;
    async function refresh(){
      controller=new AbortController();
      try {
        const response=await request('/share',{signal:controller.signal});
        if(!response.ok) throw new Error('Không thể lấy trạng thái chia sẻ.');
        const value=await response.json();
        if(!closed) setStatus(value);
      } catch(error){if(!closed && error.name!=='AbortError') setError(errorMessage(error));}
    }
    refresh();const timer=setInterval(refresh,1000);
    return()=>{closed=true;controller?.abort();clearInterval(timer);generation.current++;source.current?.stop();sender.current?.close();};
  },[]);
  function stop(){source.current?.stop();sender.current?.close();source.current=null;sender.current=null;setActive(false);setBusy(false);if(preview.current) preview.current.srcObject=null;}
  async function sendMessage(event){
    event.preventDefault();
    if(chatBusy || !active || status.state!=='connected' || !message.trim()) return;
    setChatBusy(true);setChatError('');
    try {
      await post('/share/chat',{source_id:shareID.current,message});
      setMessage('');
      const response=await request('/share');
      if(response.ok) setStatus(await response.json());
    }catch(error){setChatError(errorMessage(error));}
    finally{setChatBusy(false);}
  }
  async function start(){
    const current=++generation.current;
    setBusy(true);setError('');setStatus({state:'starting'});
    let socket,pending,opening,sourceID;
    let stopped=false;
    function close(){
      if(stopped) return;stopped=true;
      if(pending){clearTimeout(pending.timer);pending.reject(new Error('Kênh chia sẻ đã đóng.'));pending=null;}
      socket?.close();
    }
    async function sendFrame(frame,size){
      if(stopped) throw new Error('Đã dừng chia sẻ.');
      if(!opening) opening=(async()=>{
        const started=await post('/share/start',{width:size.width,height:size.height,encoding:size.encoding || 'jpeg',quality});sourceID=started.source_id;shareID.current=sourceID;
        if(stopped){await post('/share/stop',{source_id:sourceID});throw new Error('Đã dừng chia sẻ.');}
        socket=await streamSocket('', `/share/ws?source_id=${encodeURIComponent(sourceID)}`);
        if(stopped){socket.close();throw new Error('Đã dừng chia sẻ.');}
        await new Promise((resolve,reject)=>{
          const timer=setTimeout(()=>{reject(new Error('Kết nối chia sẻ quá thời gian.'));close();},10000);
          socket.onopen=()=>{clearTimeout(timer);resolve();};
          socket.onerror=socket.onclose=()=>{clearTimeout(timer);reject(new Error('Không thể kết nối kênh chia sẻ.'));close();};
        });
        socket.onmessage=event=>{if(event.data==='ack' && pending){clearTimeout(pending.timer);pending.resolve();pending=null;}};
        socket.onclose=socket.onerror=()=>{close();source.current?.stop();if(current===generation.current) setError('Kết nối chia sẻ đã bị ngắt.');};
      })();
      await opening;
      if(stopped) throw new Error('Đã dừng chia sẻ.');
      return new Promise((resolve,reject)=>{
        pending={resolve,reject,timer:setTimeout(()=>{reject(new Error('Gửi hình ảnh quá thời gian.'));close();},10000)};
        if(size.heartbeat) socket.send('ping');
        else if(size.encoding==='native-jpeg'){
          const header=new TextEncoder().encode(JSON.stringify(size));
          const length=new Uint8Array(4);new DataView(length.buffer).setUint32(0,header.length,false);
          socket.send(new Blob([length,header,frame]));
        }else socket.send(frame);
      });
    }
    sender.current={close};
    try {
      const [maxWidth,maxHeight]=resolution.split('x').map(Number);
      const capture=await startBrowserShare({fps,quality:quality/100,maxWidth,maxHeight,nativeFrames:true,sendFrame,onStop:()=>{
        close();if(sourceID) post('/share/stop',{source_id:sourceID}).catch(()=>{});
        if(current===generation.current){source.current=null;setActive(false);setBusy(false);if(preview.current) preview.current.srcObject=null;}
      },onError:error=>{console.error('Chia sẻ màn hình thất bại',error);if(current===generation.current) setError(errorMessage(error));}});
      if(current!==generation.current || stopped){capture.stop();return;}
      source.current=capture;preview.current.srcObject=capture.stream;
      setActive(true);
    } catch(error){close();console.error('Không thể bắt đầu chia sẻ màn hình',error);if(current===generation.current) {setError(errorMessage(error));setStatus({state:'stopped'});}}
    finally {if(current===generation.current) setBusy(false);}
  }
  const labels={stopped:'Đã dừng chia sẻ',starting:'Đang chọn màn hình…',registering:'Đang đăng ký ID…',ready:'Đang chờ đối tác kết nối',connecting:'Đối tác đang kết nối…',connected:'Đối tác đang xem màn hình'};
  return <div className="modal-backdrop" onClick={event=>{if(event.target===event.currentTarget && !active && !busy) onClose();}}>
    <section className="sharing-dialog" role="dialog" aria-modal="true" aria-labelledby="sharing-title">
      <div className="panel-heading"><h2 id="sharing-title">Chia sẻ màn hình của bạn</h2><button className="secondary" onClick={()=>{stop();onClose();}}>Đóng</button></div>
      <p>Gửi ID và mật khẩu bên dưới để đối tác kết nối bằng UltraViewer.</p>
      <div className="sharing-credentials"><label>ID của bạn<output>{status.id || '—'}</output></label><label>Mật khẩu<output>{status.id ? status.password : '—'}</output></label></div>
      <p className="sharing-state" role="status">{labels[status.state] || labels.stopped}</p>
      <div className="sharing-options"><label>Độ phân giải<select disabled={active || busy} value={resolution} onChange={e=>setResolution(e.target.value)}><option value="1280x720">HD · 1280 × 720</option><option value="1920x1080">Full HD · 1920 × 1080</option><option value="2560x1440">2K · 2560 × 1440</option></select></label><label>Chất lượng<select disabled={active || busy} value={quality} onChange={e=>setQuality(Number(e.target.value))}><option value={80}>80% · Nhẹ</option><option value={92}>92% · Rõ nét</option><option value={98}>98% · Rất rõ</option></select></label><label>Tốc độ<select disabled={active || busy} value={fps} onChange={e=>setFPS(Number(e.target.value))}><option value={15}>15 FPS</option><option value={30}>30 FPS</option></select></label></div>
      <div className="sharing-body" hidden={!active}>
        <video ref={preview} autoPlay muted playsInline aria-label="Xem trước màn hình đang chia sẻ"/>
        <section className="sharing-chat" aria-label="Nhắn tin với người xem">
          <h3>Nhắn tin với người xem</h3>
          <div ref={chatList} className="messages" role="log" aria-live="polite">{status.messages?.length ? status.messages.map(item=><div key={item.id} className={`message ${item.direction}`}><span>{item.direction==='incoming'?'Đối tác':'Bạn'}</span><p>{item.text}</p><small>{{queued:'Đang gửi…',sent:'Đã gửi',received:'Đã nhận',failed:'Gửi thất bại'}[item.status]}</small></div>) : <p className="help">{status.state==='connected'?'Chưa có tin nhắn.':'Chờ đối tác kết nối để nhắn tin.'}</p>}</div>
          <form className="chat-compose" onSubmit={sendMessage}><textarea aria-label="Tin nhắn chia sẻ" placeholder="Nhập tin nhắn…" value={message} onChange={event=>setMessage(event.target.value)} disabled={status.state!=='connected'} onKeyDown={event=>{if(event.key==='Enter' && !event.shiftKey){event.preventDefault();sendMessage(event);}}}/><button className="primary" disabled={status.state!=='connected' || chatBusy || !message.trim()} type="submit">Gửi</button></form>
          {chatError && <p className="error" role="alert">{chatError}</p>}
        </section>
      </div>
      <p className="help">Chỉ chia sẻ hình ảnh. Đối tác không thể điều khiển bàn phím, chuột hoặc truy cập bộ nhớ tạm. Mỗi phiên hỗ trợ một người xem.</p>
      {(error || status.error) && <p className="error" role="alert">{error || errorMessage(status.error)}</p>}
      <div className="sharing-actions">{active ? <button className="disconnect" onClick={stop}>Dừng chia sẻ</button> : <button className="primary" disabled={busy} onClick={start}>{busy?'Đang khởi tạo…':'Chọn màn hình và chia sẻ'}</button>}</div>
    </section>
  </div>;
}
