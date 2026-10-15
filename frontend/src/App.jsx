import { useEffect, useRef, useState } from 'react';
import { bootstrap, post, upload, downloadFile } from './api';
import { useRemote } from './remote';
import { useClipboard } from './clipboard';
import { errorMessage } from './errors';
import Sharing from './SharingPanel.jsx';

function Icon({ name, size = 20 }) {
  const paths = {
    monitor:<><rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4"/></>,
    chat:<path d="M21 11.5a8.5 8.5 0 0 1-8.5 8.5H4l-2 2v-9.5A8.5 8.5 0 0 1 10.5 4H13a8 8 0 0 1 8 7.5Z"/>,
    sun:<><circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5"/></>,
    moon:<path d="M20.5 14a8.5 8.5 0 0 1-10.5-10.5A9 9 0 1 0 20.5 14Z"/>,
    settings:<><path d="M4 7h16M4 17h16"/><circle cx="9" cy="7" r="3"/><circle cx="15" cy="17" r="3"/></>,
    close:<path d="m6 6 12 12M18 6 6 18"/>,
    send:<><path d="m22 2-7 20-4-9-9-4 20-7Z"/><path d="m22 2-11 11"/></>,
    clipboard:<><rect x="5" y="4" width="14" height="18" rx="2"/><rect x="8" y="2" width="8" height="4" rx="1"/><path d="M9 11h6M9 15h6"/></>,
    file:<><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6M8 13h8M8 17h5"/></>,
    expand:<path d="M9 3H3v6m12-6h6v6M3 15v6h6m6 0h6v-6"/>,
    chevron:<path d="m14 6-6 6 6 6"/>,
    check:<path d="m5 12 4 4L19 6"/>,
  };
  return <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{paths[name]}</svg>;
}
const qualityPresets = [{ value:30, name:'Ưu tiên tốc độ', detail:'Nhẹ hơn cho mạng chậm' }, { value:45, name:'Cân bằng', detail:'Hài hòa tốc độ và hình ảnh' }, { value:70, name:'Ưu tiên hình ảnh', detail:'Rõ hơn, sử dụng nhiều dữ liệu' }];
const fileLabels = { waiting_for_acceptance:'Chờ đối tác chấp nhận', sending:'Đang gửi', completed:'Đối tác đã nhận đủ tệp', canceled:'Đã hủy', failed:'Gửi thất bại' };
const messageLabels = { queued:'Đang gửi…', sent:'Đã gửi', received:'', failed:'Gửi thất bại' };
function formatSize(bytes = 0) { return bytes >= 1024*1024 ? `${(bytes/1024/1024).toFixed(1)} MB` : `${Math.ceil(bytes/1024)} KB`; }

function Settings({ settings, display, connecting, onClose, onSave }) {
  const dialog = useRef(null);
  const [draft, setDraft] = useState({ ...settings, ...display });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [section, setSection] = useState('general');
  useEffect(() => { dialog.current.showModal(); }, []);
  async function save(event) {
    event.preventDefault(); setSaving(true); setError('');
    try { await onSave(draft); onClose(); } catch (e) { setError(errorMessage(e)); } finally { setSaving(false); }
  }
  return <dialog ref={dialog} className="settings-dialog" onCancel={onClose} aria-labelledby="settings-title">
    <form onSubmit={save}>
      <div className="panel-heading"><h2 id="settings-title">Cài đặt</h2><button type="button" className="icon-button" aria-label="Đóng cài đặt" onClick={onClose}><Icon name="close"/></button></div>
      <div className="settings-tabs"><button type="button" className={section === 'general' ? 'selected' : ''} onClick={() => setSection('general')}>Chung</button><button type="button" className={section === 'image' ? 'selected' : ''} onClick={() => setSection('image')}>Hình ảnh</button><button type="button" className={section === 'network' ? 'selected' : ''} onClick={() => setSection('network')}>Mạng</button></div>
      {section === 'general' ? <><div className="settings-grid">
        <label className="field full-width">Tên máy<input value={draft.machine_name} maxLength={128} required onChange={e => setDraft({...draft,machine_name:e.target.value})}/><span className="help">Áp dụng ở lần kết nối tiếp theo.</span></label>
        <label className="field">Giao diện<select value={draft.theme || 'light'} onChange={e => setDraft({...draft,theme:e.target.value})}><option value="light">Sáng</option><option value="dark">Tối</option></select></label>
      </div>
      <div className="settings-toggles">
        <label className="switch-row"><span><strong>Chỉ xem màn hình</strong><small>Tắt điều khiển chuột và bàn phím</small></span><input type="checkbox" checked={draft.viewOnly} onChange={e => setDraft({...draft,viewOnly:e.target.checked})}/></label>
        <label className="switch-row"><span><strong>Đồng bộ bộ nhớ tạm</strong><small>Văn bản · có thể cần quyền trình duyệt</small></span><input type="checkbox" checked={!!draft.clipboard_sync} onChange={e => setDraft({...draft,clipboard_sync:e.target.checked})}/></label>
      </div>
      </> : section === 'image' ? <>
        <div className="settings-grid">
        <label className="field">Chất lượng máy đối tác<select value={draft.jpeg_quality} onChange={e => setDraft({...draft,jpeg_quality:Number(e.target.value)})}>{qualityPresets.map(q => <option key={q.value} value={q.value}>{q.name}</option>)}</select></label>
        <label className="field">Cơ chế truyền hình ảnh<select value={draft.stream_transport || 'websocket'} onChange={e => setDraft({...draft,stream_transport:e.target.value,stream_encoding:e.target.value!=='webrtc' && draft.stream_encoding==='vp8' ? 'jpeg' : draft.stream_encoding})}><option value="websocket">WebSocket</option><option value="webrtc">WebRTC</option><option value="polling">HTTP · cập nhật định kỳ</option></select></label>
        <label className="field">Định dạng hình ảnh<select disabled={draft.stream_transport === 'polling'} value={draft.stream_encoding || 'jpeg'} onChange={e => setDraft({...draft,stream_encoding:e.target.value})}><option value="jpeg">JPEG · tiết kiệm băng thông</option><option value="png">PNG · không mất dữ liệu</option>{draft.stream_transport==='webrtc' && <option value="vp8">VP8 · video thời gian thực</option>}</select></label>
        <label className="field">Chất lượng JPEG<input type="number" min={1} max={100} required disabled={draft.stream_transport === 'polling' || draft.stream_encoding === 'png' || draft.stream_encoding === 'vp8'} value={draft.stream_quality ?? 75} onChange={e => setDraft({...draft,stream_quality:Number(e.target.value)})}/></label>
        <label className="field">Tốc độ tối đa (khung/giây)<input type="number" min={1} max={60} required disabled={draft.stream_transport === 'polling'} value={draft.stream_fps ?? 30} onChange={e => setDraft({...draft,stream_fps:Number(e.target.value)})}/><span className="help">Phụ thuộc tốc độ cập nhật của máy đối tác.</span></label>
        <label className="field">Băng thông mục tiêu (kbps)<input type="number" min={100} max={50000} required disabled={draft.stream_transport === 'polling'} value={draft.stream_bitrate ?? 10000} onChange={e => setDraft({...draft,stream_bitrate:Number(e.target.value)})}/></label>
        <label className="field">Chu kỳ cập nhật dự phòng<select value={draft.interval} onChange={e => setDraft({...draft,interval:Number(e.target.value)})}><option value={250}>Nhanh · mỗi 250 ms</option><option value={500}>Tiêu chuẩn · mỗi 500 ms</option><option value={1000}>Tiết kiệm · mỗi 1 giây</option></select></label>
        </div>
        <div className="settings-toggles">
        <label className="switch-row"><span><strong>Vừa với cửa sổ</strong></span><input type="checkbox" checked={draft.fit} onChange={e => setDraft({...draft,fit:e.target.checked})}/></label>
        <label className="switch-row full-width"><span><strong>Tự điều chỉnh luồng hình ảnh</strong><small>Điều chỉnh tốc độ, chất lượng và băng thông theo kết nối.</small></span><input type="checkbox" disabled={draft.stream_transport === 'polling'} checked={!!draft.stream_adaptive} onChange={e => setDraft({...draft,stream_adaptive:e.target.checked})}/></label>
        <label className="switch-row full-width"><span><strong>Chỉ truyền vùng thay đổi</strong><small>Giảm dữ liệu truyền; làm mới toàn màn hình mỗi 5 giây.</small></span><input type="checkbox" disabled={draft.stream_transport === 'polling' || draft.stream_encoding === 'vp8'} checked={!!draft.stream_dirty} onChange={e => setDraft({...draft,stream_dirty:e.target.checked})}/></label>
        </div>
        <p className="help full-width">VP8 cần FFmpeg trên máy chủ. Tự chuyển sang WebSocket hoặc cập nhật định kỳ khi mất kết nối.</p>
      </> : <>
        <div className="settings-grid">
          <label className="field">Máy chủ đăng ký<input value={draft.registration_host || ''} placeholder="functions3.ultraviewer.net" onChange={e => setDraft({...draft,registration_host:e.target.value})}/></label>
          <label className="field">Tên máy chủ TLS<input value={draft.tls_server_name || ''} placeholder="ultraviewer" onChange={e => setDraft({...draft,tls_server_name:e.target.value})}/></label>
        </div>
        <label className="switch-row proxy-switch"><span><strong>Máy chủ trung gian SOCKS5</strong></span><input type="checkbox" checked={!!draft.socks5_enabled} onChange={e => setDraft({...draft,socks5_enabled:e.target.checked})}/></label>
        {draft.socks5_enabled && <div className="settings-grid">
          <label className="field">Địa chỉ máy chủ SOCKS5<input value={draft.socks5_host || ''} placeholder="127.0.0.1" required onChange={e => setDraft({...draft,socks5_host:e.target.value})}/></label>
          <label className="field">Cổng<input type="number" min={1} max={65535} value={draft.socks5_port ?? 1080} required onChange={e => setDraft({...draft,socks5_port:Number(e.target.value)})}/></label>
          <label className="field">Tên đăng nhập<input value={draft.socks5_username || ''} autoComplete="off" onChange={e => setDraft({...draft,socks5_username:e.target.value})}/></label>
          <label className="field">Mật khẩu<input type="password" value={draft.socks5_password || ''} autoComplete="new-password" onChange={e => setDraft({...draft,socks5_password:e.target.value})}/></label>
        </div>}
        <p className="help">Áp dụng ở lần kết nối tiếp theo. Để trống tên đăng nhập/mật khẩu nếu máy chủ trung gian không yêu cầu đăng nhập.</p>
      </>}
      {error && <p className="error" role="alert">{error}</p>}
      {connecting && <p className="help">Chờ kết nối hoàn tất trước khi lưu cài đặt.</p>}
      <div className="dialog-actions"><button type="button" className="secondary" onClick={onClose}>Đóng</button><button className="primary" disabled={saving || connecting}>{saving ? 'Đang lưu…' : 'Lưu cài đặt'}</button></div>
    </form>
  </dialog>;
}

function Communication({ status, connected, onClose, refresh, hidden }) {
  const [tab, setTab] = useState('chat');
  const [text, setText] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [fileBusy, setFileBusy] = useState('');
  const list = useRef(null);
  const panel = useRef(null);
  const drag = useRef(null);
  const [position, setPosition] = useState(null);
  const [dragging, setDragging] = useState(false);
  function boundedPosition(x, y) {
    const box=panel.current.getBoundingClientRect();
    const top=document.querySelector('.app.remote-connected') ? 8 : document.querySelector('.control-bar').getBoundingClientRect().bottom+8;
    const minY=Math.min(top,Math.max(8,window.innerHeight-box.height-8));
    return {x:Math.max(8,Math.min(x,window.innerWidth-box.width-8)),y:Math.max(minY,Math.min(y,window.innerHeight-box.height-8))};
  }
  useEffect(() => {
    if(hidden) return;
    const clamp=()=>setPosition(value=>value ? boundedPosition(value.x,value.y) : value);
    clamp(); window.addEventListener('resize',clamp);
    return ()=>window.removeEventListener('resize',clamp);
  }, [hidden]);
  function startDrag(event) {
    if(event.button!==0 || event.target.closest('button')) return;
    const box=panel.current.getBoundingClientRect();
    drag.current={id:event.pointerId,x:event.clientX-box.left,y:event.clientY-box.top};
    setPosition({x:box.left,y:box.top});setDragging(true);
    event.currentTarget.setPointerCapture(event.pointerId);event.preventDefault();
  }
  function moveDrag(event) {
    if(drag.current?.id!==event.pointerId) return;
    setPosition(boundedPosition(event.clientX-drag.current.x,event.clientY-drag.current.y));
  }
  function finishDrag() {drag.current=null;setDragging(false);}
  const uploadAbort = useRef(null);
  const transfer = status.transfer;
  const active = transfer && !['completed','canceled','failed'].includes(transfer.status);
  useEffect(() => { if (list.current) list.current.scrollTop = list.current.scrollHeight; }, [status.messages?.length, tab]);
  useEffect(() => () => uploadAbort.current?.abort(), []);
  async function send(event) {
    event.preventDefault(); setBusy(true); setError('');
    try { await post('/chat', { message:text }); setText(''); refresh(); } catch (e) { setError(errorMessage(e)); } finally { setBusy(false); }
  }
  async function sendFile(event) {
    const file = event.target.files[0]; event.target.value = ''; if (!file) return;
    setError('');
    if (!file.size || file.size > 50*1024*1024) { setError('Chọn tệp có dung lượng từ 1 byte đến 50 MB.'); return; }
    setUploading(true); uploadAbort.current = new AbortController();
    try { await upload(file, uploadAbort.current.signal); refresh(); } catch (e) { if (e.name !== 'AbortError') setError(errorMessage(e)); } finally { setUploading(false); }
  }
  async function incomingAction(file, action) {
    setFileBusy(file.id);setError('');
    try {
      if(action==='download') await downloadFile(file.id,file.name);
      else {await post(action==='accept'?'/file/accept':'/file/cancel',{id:file.id});refresh();}
    } catch(e){setError(errorMessage(e));} finally {setFileBusy('');}
  }
  async function cancel() { setError(''); try { await post('/file/cancel'); refresh(); } catch (e) { setError(errorMessage(e)); } }
  return <section ref={panel} hidden={hidden} style={position ? {position:'fixed',left:position.x,top:position.y} : undefined} className={`communication-panel ${dragging ? 'dragging' : ''}`} aria-label="Nhắn tin và gửi tệp">
    <div className="communication-topbar drag-handle" title="Kéo để di chuyển" onPointerDown={startDrag} onPointerMove={moveDrag} onPointerUp={finishDrag} onPointerCancel={finishDrag} onLostPointerCapture={finishDrag}>
      <span className="drag-grip" aria-hidden="true">⠿</span>
      <div className="tabs" role="tablist"><button role="tab" aria-selected={tab === 'chat'} onClick={() => setTab('chat')}>Tin nhắn</button><button role="tab" aria-selected={tab === 'files'} onClick={() => setTab('files')}>Tệp{status.incoming?.some(file=>file.status==='offered') && <span className="file-dot"/>}</button></div>
      <button className="icon-button" onClick={onClose} aria-label="Ẩn nhắn tin và gửi tệp"><Icon name="close" size={16}/></button>
    </div>
    {tab === 'chat' ? <><div ref={list} className="messages" role="log" aria-live="polite">{status.messages?.length ? status.messages.map(m => <div key={m.id} className={`message ${m.direction}`}><span>{m.direction === 'incoming' ? 'Đối tác' : 'Bạn'}</span><p>{m.text}</p><small>{messageLabels[m.status] || ''}</small></div>) : <div className="panel-empty"><p>{connected ? "Chưa có tin nhắn" : "Kết nối để nhắn tin"}</p></div>}</div><form className="chat-compose" onSubmit={send}><textarea aria-label="Tin nhắn" placeholder="Nhập tin nhắn…" value={text} onChange={e => setText(e.target.value)} disabled={!connected} onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); if (connected && !busy && text.trim()) send(e); } }}/><div><small>Enter để gửi</small><button className="primary icon-button" type="submit" disabled={!connected || busy || !text.trim()} aria-label="Gửi tin nhắn"><Icon name="send" size={18}/></button></div></form></> : <div className="files-content"><label className={`file-picker ${!connected || active || uploading ? 'disabled' : ''}`}><Icon name="file" size={32}/><strong>{uploading ? 'Đang tải lên máy chủ…' : 'Chọn tệp để gửi'}</strong><span>Tối đa 50 MB · mỗi lần một tệp</span><input type="file" aria-label="Chọn tệp để gửi" disabled={!connected || active || uploading} onChange={sendFile}/></label><p className="help">Đối tác cần chấp nhận lưu tệp trước khi bắt đầu truyền.</p>{transfer && <div className="transfer-card"><div className="file-info"><Icon name="file"/><div><strong>{transfer.name}</strong><small>{formatSize(transfer.size)}</small></div></div><progress max={transfer.size || 1} value={transfer.acknowledged || 0}/><div className="transfer-detail"><span>{fileLabels[transfer.status] || 'Đang xử lý'}</span><span>{Math.round(100*(transfer.acknowledged || 0)/(transfer.size || 1))}%</span></div>{transfer.error && <p className="error">{errorMessage(transfer.error)}</p>}{active && <button className="secondary" disabled={!connected} onClick={cancel}>Hủy gửi tệp</button>}</div>}
      {status.incoming?.length>0 && <div className="received-files"><span className="help">Tệp từ đối tác</span>{status.incoming.map(file=><div key={file.id} className="transfer-card incoming-card">
        <div className="file-info"><Icon name="file"/><div><strong>{file.name}</strong><small>{formatSize(file.size)}</small></div></div>
        <progress max={file.size || 1} value={file.received || 0}/>
        <div className="transfer-detail"><span>{{offered:'Chờ bạn chấp nhận',receiving:'Đang nhận',completed:'Đã nhận đủ tệp',canceled:'Đã hủy',failed:'Nhận thất bại'}[file.status]}</span><span>{Math.round(100*(file.received || 0)/(file.size || 1))}%</span></div>
        {file.error && <p className="error">{errorMessage(file.error)}</p>}
        <div className="incoming-actions">
          {file.status==='offered' && <><button className="primary" disabled={!connected || fileBusy===file.id} onClick={()=>incomingAction(file,'accept')}>Nhận file</button><button className="secondary" disabled={!connected || fileBusy===file.id} onClick={()=>incomingAction(file,'cancel')}>Từ chối</button></>}
          {file.status==='receiving' && <button className="secondary" disabled={!connected || fileBusy===file.id} onClick={()=>incomingAction(file,'cancel')}>Hủy nhận</button>}
          {file.status==='completed' && <button className="primary" disabled={fileBusy===file.id} onClick={()=>incomingAction(file,'download')}>{fileBusy===file.id?'Đang tải…':'Tải về máy'}</button>}
        </div>
      </div>)}</div>}
    </div>}
    {error && <div className="panel-error error" role="alert">{error}</div>}
  </section>;
}

export default function App() {
  const [settings, setSettings] = useState(null);
  const [display, setDisplay] = useState({interval:500,fit:true,viewOnly:false});
  const beforePaste = useRef(null);
  const remote = useRemote(display.interval, display.viewOnly, beforePaste, settings);
  const [partner, setPartner] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [sharingOpen, setSharingOpen] = useState(false);
  const [chatOpen, setChatOpen] = useState(false);
  const [unread, setUnread] = useState(0);
  const [notice, setNotice] = useState('');
  const seenMessages = useRef(new Set());
  const [toolbarShown, setToolbarShown] = useState(false);
  const [toolbarAnimated,setToolbarAnimated]=useState(false);
  useEffect(()=>{if(!remote.ready) return;let second;const first=requestAnimationFrame(()=>{second=requestAnimationFrame(()=>setToolbarAnimated(true));});return()=>{cancelAnimationFrame(first);cancelAnimationFrame(second);};},[remote.ready]);
  const [toolbarFocused, setToolbarFocused] = useState(false);
  const toolbarTimer = useRef();
  const connected = remote.status.state === 'connected' && remote.online;
  const active = remote.status.state !== 'disconnected';
  const clipboard = useClipboard(connected,settings?.clipboard_sync || false,display.viewOnly);
  beforePaste.current=settings?.clipboard_sync ? clipboard.sendFromLocal : null;
  useEffect(() => { if(remote.status.partner_id) setPartner(remote.status.partner_id); }, [remote.status.partner_id]);
  useEffect(() => { setToolbarShown(false); setToolbarFocused(false); clearTimeout(toolbarTimer.current); }, [connected]);
  useEffect(() => () => clearTimeout(toolbarTimer.current), []);
  function showToolbar() { clearTimeout(toolbarTimer.current); setToolbarShown(true); }
  function hideToolbar() { clearTimeout(toolbarTimer.current); toolbarTimer.current=setTimeout(() => setToolbarShown(false),600); }
  useEffect(() => { bootstrap().then(value => {setSettings(value);setDisplay({interval:value.refresh_interval || 500,fit:value.screen_fit ?? true,viewOnly:!!value.view_only});}).catch(e => setError(errorMessage(e))); }, []);
  useEffect(() => { if(settings) document.documentElement.dataset.theme=settings.theme || 'light'; }, [settings?.theme]);
  useEffect(() => { if (!notice) return; const timer=setTimeout(() => setNotice(''),4000); return () => clearTimeout(timer); }, [notice]);
  useEffect(() => {
    const messages=remote.status.messages || [];
    const files=remote.status.incoming || [];
    const count=messages.filter(m => m.direction === 'incoming' && !seenMessages.current.has(m.id)).length + files.filter(f=>f.status==='offered' && !seenMessages.current.has('file:'+f.id)).length;
    seenMessages.current=new Set([...messages.map(m=>m.id),...files.map(f=>'file:'+f.id)]);
    if (chatOpen) setUnread(0); else if (count) setUnread(value => value + count);
  }, [remote.status.messages, remote.status.incoming, chatOpen]);
  async function connect(event) {
    event.preventDefault(); setBusy(true); setError('');
    const id=partner.replace(/\s/g, '');
    try { await post('/connect',{ id, pass:password }); remote.refresh(); } catch (e) { setError(errorMessage(e)); } finally { setBusy(false); }
  }
  async function disconnect() { setBusy(true); setError(''); try { await remote.disconnect(); } catch (e) { setError(errorMessage(e)); } finally { setBusy(false); } }
  async function saveSettings(draft) {
    const next={ stream_adaptive:!!draft.stream_adaptive,stream_bitrate:Number(draft.stream_bitrate || 10000),stream_transport:draft.stream_transport || "websocket",stream_encoding:draft.stream_encoding || "jpeg",stream_quality:Number(draft.stream_quality || 75),stream_fps:Number(draft.stream_fps || 30),stream_dirty:!!draft.stream_dirty, machine_name:draft.machine_name, jpeg_quality:draft.jpeg_quality, clipboard_sync:!!draft.clipboard_sync,theme:draft.theme || "light",refresh_interval:draft.interval,screen_fit:draft.fit,view_only:draft.viewOnly,registration_host:draft.registration_host?.trim() || 'functions3.ultraviewer.net',tls_server_name:draft.tls_server_name?.trim() || 'ultraviewer',socks5_enabled:!!draft.socks5_enabled,socks5_host:draft.socks5_host?.trim() || '',socks5_port:draft.socks5_port || 1080,socks5_username:draft.socks5_username || '',socks5_password:draft.socks5_password || '' };
    await post('/settings',next); setSettings(next);
    const local={ interval:draft.interval, fit:draft.fit, viewOnly:draft.viewOnly };
    setDisplay(local); setNotice('Đã lưu cài đặt');
  }
  async function toggleFit() {
    if (!settings) return;
    const next={...settings,screen_fit:!display.fit};
    try {await post('/settings',next);setSettings(next);setDisplay({...display,fit:next.screen_fit});} catch(e){setError(errorMessage(e));}
  }
  async function toggleTheme() {
    if (!settings) return;
    const next={...settings,theme:settings.theme==='dark'?'light':'dark'};
    try {await post('/settings',next);setSettings(next);} catch(e){setError(errorMessage(e));}
  }
  const stateLabel = !remote.online ? 'Mất kết nối máy chủ' : { disconnected:'Chưa kết nối', connecting:'Đang kết nối', connected:'Đã kết nối' }[remote.status.state];
  return <div className={`app ${connected ? 'remote-connected' : ''} ${remote.ready ? '' : 'initializing'} ${toolbarAnimated ? 'toolbar-animated' : ''}`}>
    {connected && <button className="top-edge" aria-label="Hiện thanh điều khiển" onPointerEnter={showToolbar} onPointerLeave={hideToolbar} onClick={showToolbar} onFocus={showToolbar}><span/></button>}
    <header className={`control-bar ${toolbarShown || toolbarFocused || settingsOpen || sharingOpen ? 'revealed' : ''}`}
      onPointerEnter={showToolbar} onPointerLeave={hideToolbar}
      onFocusCapture={() => setToolbarFocused(true)} onBlurCapture={event => { if (!event.currentTarget.contains(event.relatedTarget)) setToolbarFocused(false); }}
      inert={connected && !toolbarShown && !toolbarFocused && !settingsOpen && !sharingOpen ? true : undefined}>
      <span className="brand"><span className="brand-mark"><Icon name="monitor" size={19}/></span>AnyViewer</span>
      {!connected && <form className="connect-form" onSubmit={connect}>
        <input aria-label="ID đối tác" inputMode="numeric" autoComplete="off" placeholder="ID đối tác" required value={partner} disabled={active} onChange={e => setPartner(e.target.value.replace(/\s/g, ''))}/>
        <input aria-label="Mật khẩu" type="password" autoComplete="off" placeholder="Mật khẩu" required value={password} disabled={active} onChange={e => setPassword(e.target.value)}/>
        {active ? <button type="button" className="disconnect" onClick={disconnect} disabled={busy}>Hủy kết nối</button> : <button className="primary" disabled={busy || !settings}>{busy ? 'Đang kết nối…' : 'Kết nối'}</button>}
      </form>}
      <span className={`status-badge ${connected ? 'connected' : active ? 'connecting' : ''}`}><span className={active && !connected ? 'pulse' : ''}/>{connected ? partner || stateLabel : stateLabel}</span>
      <div className="view-actions">
        <button className="secondary compact" onClick={()=>setSharingOpen(true)} disabled={!settings}>Chia sẻ</button>
        <span className="view-tag" title={remote.streamError || (remote.status.stream ? `Định dạng: ${remote.status.stream.encoding.toUpperCase()} · ${remote.status.stream.fps.toFixed(0)} khung/giây · ${remote.status.stream.bitrate_kbps} kbps` : "Cơ chế truyền hình ảnh đang sử dụng")}>{{websocket:'WebSocket',webrtc:'WebRTC',polling:'Cập nhật định kỳ'}[remote.transport]}{remote.status.stream && ` · ${remote.status.stream.encoding.toUpperCase()}`}</span>
        {display.viewOnly && <span className="view-tag">Chỉ xem</span>}
        <button className="icon-button" title={display.fit ? 'Kích thước thật' : 'Vừa cửa sổ'} aria-label={display.fit ? 'Chuyển sang kích thước thật' : 'Chuyển sang vừa cửa sổ'} onClick={toggleFit} aria-pressed={display.fit}><Icon name="monitor"/></button>
        <button className="icon-button" title="Toàn màn hình" aria-label="Toàn màn hình" onClick={() => (document.fullscreenElement ? document.exitFullscreen() : document.documentElement.requestFullscreen()).catch(e => setError(errorMessage(e)))}><Icon name="expand"/></button>
        <button className="icon-button" title={settings?.theme==='dark'?'Giao diện sáng':'Giao diện tối'} aria-label={settings?.theme==='dark'?'Giao diện sáng':'Giao diện tối'} onClick={toggleTheme} disabled={!settings || remote.status.state==='connecting'}><Icon name={settings?.theme==='dark'?'sun':'moon'}/></button>
        <button className="icon-button" title="Cài đặt" aria-label="Cài đặt" onClick={() => setSettingsOpen(true)} disabled={!settings}><Icon name="settings"/></button>
        {connected && settings?.clipboard_sync && <>
          <button className="icon-button" title="Gửi bộ nhớ tạm tới đối tác" aria-label="Gửi bộ nhớ tạm tới đối tác" disabled={display.viewOnly} onClick={() => clipboard.sendFromLocal().then(ok => {if(ok) setNotice('Đã gửi bộ nhớ tạm tới đối tác');})}><Icon name="clipboard"/></button>
          <button className={`icon-button ${clipboard.pending ? 'clipboard-pending' : ''}`} title="Nhận bộ nhớ tạm từ đối tác" aria-label="Nhận bộ nhớ tạm từ đối tác" disabled={!clipboard.pending} onClick={() => clipboard.receiveFromRemote().then(ok => {if(ok) setNotice('Đã nhận bộ nhớ tạm từ đối tác');})}><Icon name="clipboard"/><span className="clipboard-arrow">↓</span></button>
        </>}
        {connected && <button className="disconnect compact" onClick={disconnect} disabled={busy}>Ngắt kết nối</button>}
      </div>
    </header>
    {remote.streamError && <div className="error-banner" role="status">{remote.streamError}</div>}
    {(error || remote.status.error) && <div className="error-banner" role="alert"><span>{error || errorMessage(remote.status.error)}</span>{error && <button className="icon-button" aria-label="Đóng thông báo lỗi" onClick={() => setError('')}><Icon name="close" size={16}/></button>}</div>}
    <main className="workspace">
      <div className={`screen-stage ${display.fit ? 'fit' : 'actual'}`}>
        <canvas ref={remote.canvas} tabIndex={connected && !display.viewOnly ? 0 : -1} aria-label="Màn hình máy tính từ xa" className={remote.screenSize ? '' : 'hidden'}/>
        {!remote.screenSize && <div className="screen-empty"><Icon name="monitor" size={44}/><p>{active ? 'Đang chờ màn hình đối tác…' : 'Nhập ID và mật khẩu để kết nối'}</p></div>}
      </div>
    </main>
    {clipboard.error && connected && settings?.clipboard_sync && <div className="clipboard-notice" role="status"><span>{clipboard.error}</span>{clipboard.pending && <button className="secondary compact" onClick={clipboard.receiveFromRemote}>Nhận bộ nhớ tạm</button>}<button className="icon-button" aria-label="Đóng thông báo clipboard" onClick={clipboard.clearError}><Icon name="close" size={16}/></button></div>}
    {notice && <div className="toast" role="status"><Icon name="check" size={18}/>{notice}</div>}
    <div className="floating-tools"><Communication hidden={!chatOpen} status={remote.status} connected={connected} refresh={remote.refresh} onClose={() => setChatOpen(false)}/><button className={`chat-toggle ${chatOpen ? 'opened' : ''}`} onClick={() => setChatOpen(!chatOpen)} aria-label={chatOpen ? 'Ẩn nhắn tin và gửi tệp' : 'Hiện nhắn tin và gửi tệp'} title={chatOpen ? 'Ẩn nhắn tin và gửi tệp' : 'Hiện nhắn tin và gửi tệp'} aria-expanded={chatOpen}><Icon name="chevron" size={18}/>{!chatOpen && unread > 0 && <span className="unread">{unread}</span>}</button></div>
    {settingsOpen && <Settings settings={settings} display={display} connecting={remote.status.state === 'connecting'} onClose={() => setSettingsOpen(false)} onSave={saveSettings}/>}
    {sharingOpen && <Sharing onClose={()=>setSharingOpen(false)}/>}
  </div>;
}
