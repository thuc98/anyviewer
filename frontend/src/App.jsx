import { useEffect, useRef, useState } from 'react';
import { bootstrap, post, upload, downloadFile } from './api';
import { useRemote } from './remote';
import { useClipboard } from './clipboard';

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
const fileLabels = { waiting_for_acceptance:'Chờ đối tác chấp nhận', sending:'Đang gửi', completed:'Đối tác đã nhận đủ file', canceled:'Đã hủy', failed:'Gửi thất bại' };
const messageLabels = { queued:'Đang gửi…', sent:'Đã gửi', received:'', failed:'Gửi thất bại' };
function formatSize(bytes = 0) { return bytes >= 1024*1024 ? `${(bytes/1024/1024).toFixed(1)} MB` : `${Math.ceil(bytes/1024)} KB`; }

function Settings({ settings, display, connecting, onClose, onSave }) {
  const dialog = useRef(null);
  const [draft, setDraft] = useState({ ...settings, ...display });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => { dialog.current.showModal(); }, []);
  async function save(event) {
    event.preventDefault(); setSaving(true); setError('');
    try { await onSave(draft); onClose(); } catch (e) { setError(e.message); } finally { setSaving(false); }
  }
  return <dialog ref={dialog} className="settings-dialog" onCancel={onClose} aria-labelledby="settings-title">
    <form onSubmit={save}>
      <div className="panel-heading"><h2 id="settings-title">Cài đặt</h2><button type="button" className="icon-button" aria-label="Đóng cài đặt" onClick={onClose}><Icon name="close"/></button></div>
      <div className="settings-grid">
        <label className="field full-width">Tên máy<input value={draft.machine_name} maxLength={128} required onChange={e => setDraft({...draft,machine_name:e.target.value})}/><span className="help">Áp dụng ở lần kết nối tiếp theo.</span></label>
        <label className="field">Giao diện<select value={draft.theme || 'light'} onChange={e => setDraft({...draft,theme:e.target.value})}><option value="light">Light · Sáng</option><option value="dark">Dark · Tối</option></select></label>
        <label className="field">Chất lượng remote<select value={draft.jpeg_quality} onChange={e => setDraft({...draft,jpeg_quality:Number(e.target.value)})}>{qualityPresets.map(q => <option key={q.value} value={q.value}>{q.name}</option>)}</select></label>
        <label className="field full-width">Cập nhật màn hình<select value={draft.interval} onChange={e => setDraft({...draft,interval:Number(e.target.value)})}><option value={250}>Nhanh · mỗi 250 ms</option><option value={500}>Tiêu chuẩn · mỗi 500 ms</option><option value={1000}>Tiết kiệm · mỗi 1 giây</option></select></label>
      </div>
      <div className="settings-toggles">
        <label className="switch-row"><span><strong>Vừa với cửa sổ</strong></span><input type="checkbox" checked={draft.fit} onChange={e => setDraft({...draft,fit:e.target.checked})}/></label>
        <label className="switch-row"><span><strong>Chỉ xem màn hình</strong><small>Tắt điều khiển chuột và bàn phím</small></span><input type="checkbox" checked={draft.viewOnly} onChange={e => setDraft({...draft,viewOnly:e.target.checked})}/></label>
        <label className="switch-row"><span><strong>Đồng bộ clipboard</strong><small>Văn bản · có thể cần quyền trình duyệt</small></span><input type="checkbox" checked={!!draft.clipboard_sync} onChange={e => setDraft({...draft,clipboard_sync:e.target.checked})}/></label>
      </div>
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
    try { await post('/chat', { message:text }); setText(''); refresh(); } catch (e) { setError(e.message); } finally { setBusy(false); }
  }
  async function sendFile(event) {
    const file = event.target.files[0]; event.target.value = ''; if (!file) return;
    setError('');
    if (!file.size || file.size > 50*1024*1024) { setError('Chọn file có dung lượng từ 1 byte đến 50 MB.'); return; }
    setUploading(true); uploadAbort.current = new AbortController();
    try { await upload(file, uploadAbort.current.signal); refresh(); } catch (e) { if (e.name !== 'AbortError') setError(e.message); } finally { setUploading(false); }
  }
  async function incomingAction(file, action) {
    setFileBusy(file.id);setError('');
    try {
      if(action==='download') await downloadFile(file.id,file.name);
      else {await post(action==='accept'?'/file/accept':'/file/cancel',{id:file.id});refresh();}
    } catch(e){setError(e.message);} finally {setFileBusy('');}
  }
  async function cancel() { setError(''); try { await post('/file/cancel'); refresh(); } catch (e) { setError(e.message); } }
  return <section ref={panel} hidden={hidden} style={position ? {position:'fixed',left:position.x,top:position.y} : undefined} className={`communication-panel ${dragging ? 'dragging' : ''}`} aria-label="Chat và gửi file">
    <div className="communication-topbar drag-handle" title="Kéo để di chuyển" onPointerDown={startDrag} onPointerMove={moveDrag} onPointerUp={finishDrag} onPointerCancel={finishDrag} onLostPointerCapture={finishDrag}>
      <span className="drag-grip" aria-hidden="true">⠿</span>
      <div className="tabs" role="tablist"><button role="tab" aria-selected={tab === 'chat'} onClick={() => setTab('chat')}>Tin nhắn</button><button role="tab" aria-selected={tab === 'files'} onClick={() => setTab('files')}>File{status.incoming?.some(file=>file.status==='offered') && <span className="file-dot"/>}</button></div>
      <button className="icon-button" onClick={onClose} aria-label="Ẩn chat và gửi file"><Icon name="close" size={16}/></button>
    </div>
    {tab === 'chat' ? <><div ref={list} className="messages" role="log" aria-live="polite">{status.messages?.length ? status.messages.map(m => <div key={m.id} className={`message ${m.direction}`}><span>{m.direction === 'incoming' ? 'Đối tác' : 'Bạn'}</span><p>{m.text}</p><small>{messageLabels[m.status] || ''}</small></div>) : <div className="panel-empty"><p>{connected ? "Chưa có tin nhắn" : "Kết nối để chat"}</p></div>}</div><form className="chat-compose" onSubmit={send}><textarea aria-label="Tin nhắn" placeholder="Nhập tin nhắn…" value={text} onChange={e => setText(e.target.value)} disabled={!connected} onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); if (connected && !busy && text.trim()) send(e); } }}/><div><small>Enter để gửi</small><button className="primary icon-button" type="submit" disabled={!connected || busy || !text.trim()} aria-label="Gửi tin nhắn"><Icon name="send" size={18}/></button></div></form></> : <div className="files-content"><label className={`file-picker ${!connected || active || uploading ? 'disabled' : ''}`}><Icon name="file" size={32}/><strong>{uploading ? 'Đang tải lên controller…' : 'Chọn file để gửi'}</strong><span>Tối đa 50 MB · mỗi lần một file</span><input type="file" aria-label="Chọn file để gửi" disabled={!connected || active || uploading} onChange={sendFile}/></label><p className="help">Đối tác cần chấp nhận lưu file trước khi bắt đầu truyền.</p>{transfer && <div className="transfer-card"><div className="file-info"><Icon name="file"/><div><strong>{transfer.name}</strong><small>{formatSize(transfer.size)}</small></div></div><progress max={transfer.size || 1} value={transfer.acknowledged || 0}/><div className="transfer-detail"><span>{fileLabels[transfer.status] || transfer.status}</span><span>{Math.round(100*(transfer.acknowledged || 0)/(transfer.size || 1))}%</span></div>{transfer.error && <p className="error">{transfer.error}</p>}{active && <button className="secondary" disabled={!connected} onClick={cancel}>Hủy gửi file</button>}</div>}
      {status.incoming?.length>0 && <div className="received-files"><span className="help">File từ đối tác</span>{status.incoming.map(file=><div key={file.id} className="transfer-card incoming-card">
        <div className="file-info"><Icon name="file"/><div><strong>{file.name}</strong><small>{formatSize(file.size)}</small></div></div>
        <progress max={file.size || 1} value={file.received || 0}/>
        <div className="transfer-detail"><span>{{offered:'Chờ bạn chấp nhận',receiving:'Đang nhận',completed:'Đã nhận đủ file',canceled:'Đã hủy',failed:'Nhận thất bại'}[file.status]}</span><span>{Math.round(100*(file.received || 0)/(file.size || 1))}%</span></div>
        {file.error && <p className="error">{file.error}</p>}
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
  const remote = useRemote(display.interval, display.viewOnly, beforePaste);
  const [partner, setPartner] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [chatOpen, setChatOpen] = useState(false);
  const [unread, setUnread] = useState(0);
  const [notice, setNotice] = useState('');
  const seenMessages = useRef(new Set());
  const [toolbarShown, setToolbarShown] = useState(false);
  const [toolbarFocused, setToolbarFocused] = useState(false);
  const toolbarTimer = useRef();
  const connected = remote.status.state === 'connected' && remote.online;
  const active = remote.status.state !== 'disconnected';
  const clipboard = useClipboard(connected,settings?.clipboard_sync || false,display.viewOnly);
  beforePaste.current=settings?.clipboard_sync ? clipboard.sendFromLocal : null;
  useEffect(() => { setToolbarShown(false); setToolbarFocused(false); clearTimeout(toolbarTimer.current); }, [connected]);
  useEffect(() => () => clearTimeout(toolbarTimer.current), []);
  function showToolbar() { clearTimeout(toolbarTimer.current); setToolbarShown(true); }
  function hideToolbar() { clearTimeout(toolbarTimer.current); toolbarTimer.current=setTimeout(() => setToolbarShown(false),600); }
  useEffect(() => { bootstrap().then(value => {setSettings(value);setDisplay({interval:value.refresh_interval || 500,fit:value.screen_fit ?? true,viewOnly:!!value.view_only});}).catch(e => setError(e.message)); }, []);
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
    try { await post('/connect',{ id, pass:password }); remote.refresh(); } catch (e) { setError(e.message); } finally { setBusy(false); }
  }
  async function disconnect() { setBusy(true); setError(''); try { await remote.disconnect(); } catch (e) { setError(e.message); } finally { setBusy(false); } }
  async function saveSettings(draft) {
    const next={ machine_name:draft.machine_name, jpeg_quality:draft.jpeg_quality, clipboard_sync:!!draft.clipboard_sync,theme:draft.theme || "light",refresh_interval:draft.interval,screen_fit:draft.fit,view_only:draft.viewOnly };
    await post('/settings',next); setSettings(next);
    const local={ interval:draft.interval, fit:draft.fit, viewOnly:draft.viewOnly };
    setDisplay(local); setNotice('Đã lưu cài đặt');
  }
  async function toggleFit() {
    if (!settings) return;
    const next={...settings,screen_fit:!display.fit};
    try {await post('/settings',next);setSettings(next);setDisplay({...display,fit:next.screen_fit});} catch(e){setError(e.message);}
  }
  async function toggleTheme() {
    if (!settings) return;
    const next={...settings,theme:settings.theme==='dark'?'light':'dark'};
    try {await post('/settings',next);setSettings(next);} catch(e){setError(e.message);}
  }
  const stateLabel = !remote.online ? 'Mất kết nối server' : { disconnected:'Chưa kết nối', connecting:'Đang kết nối', connected:'Đã kết nối' }[remote.status.state];
  return <div className={`app ${connected ? 'remote-connected' : ''}`}>
    {connected && <button className="top-edge" aria-label="Hiện thanh điều khiển" onPointerEnter={showToolbar} onPointerLeave={hideToolbar} onClick={showToolbar} onFocus={showToolbar}><span/></button>}
    <header className={`control-bar ${toolbarShown || toolbarFocused || settingsOpen ? 'revealed' : ''}`}
      onPointerEnter={showToolbar} onPointerLeave={hideToolbar}
      onFocusCapture={() => setToolbarFocused(true)} onBlurCapture={event => { if (!event.currentTarget.contains(event.relatedTarget)) setToolbarFocused(false); }}
      inert={connected && !toolbarShown && !toolbarFocused && !settingsOpen ? true : undefined}>
      <span className="brand"><span className="brand-mark"><Icon name="monitor" size={19}/></span>AnyViewer</span>
      {!connected && <form className="connect-form" onSubmit={connect}>
        <input aria-label="Partner ID" inputMode="numeric" autoComplete="off" placeholder="Partner ID" required value={partner} disabled={active} onChange={e => setPartner(e.target.value.replace(/\s/g, ''))}/>
        <input aria-label="Mật khẩu" type="password" autoComplete="off" placeholder="Mật khẩu" required value={password} disabled={active} onChange={e => setPassword(e.target.value)}/>
        {active ? <button type="button" className="disconnect" onClick={disconnect} disabled={busy}>Hủy kết nối</button> : <button className="primary" disabled={busy || !settings}>{busy ? 'Đang kết nối…' : 'Kết nối'}</button>}
      </form>}
      <span className={`status-badge ${connected ? 'connected' : active ? 'connecting' : ''}`}><span className={active && !connected ? 'pulse' : ''}/>{connected ? partner || stateLabel : stateLabel}</span>
      <div className="view-actions">
        {display.viewOnly && <span className="view-tag">Chỉ xem</span>}
        <button className="icon-button" title={display.fit ? 'Kích thước thật' : 'Vừa cửa sổ'} aria-label={display.fit ? 'Chuyển sang kích thước thật' : 'Chuyển sang vừa cửa sổ'} onClick={toggleFit} aria-pressed={display.fit}><Icon name="monitor"/></button>
        <button className="icon-button" title="Toàn màn hình" aria-label="Toàn màn hình" onClick={() => (document.fullscreenElement ? document.exitFullscreen() : document.documentElement.requestFullscreen()).catch(e => setError(e.message))}><Icon name="expand"/></button>
        <button className="icon-button" title={settings?.theme==='dark'?'Giao diện sáng':'Giao diện tối'} aria-label={settings?.theme==='dark'?'Giao diện sáng':'Giao diện tối'} onClick={toggleTheme} disabled={!settings || remote.status.state==='connecting'}><Icon name={settings?.theme==='dark'?'sun':'moon'}/></button>
        <button className="icon-button" title="Cài đặt" aria-label="Cài đặt" onClick={() => setSettingsOpen(true)} disabled={!settings}><Icon name="settings"/></button>
        {connected && settings?.clipboard_sync && <>
          <button className="icon-button" title="Gửi clipboard tới đối tác" aria-label="Gửi clipboard tới đối tác" disabled={display.viewOnly} onClick={() => clipboard.sendFromLocal().then(ok => {if(ok) setNotice('Đã gửi clipboard tới đối tác');})}><Icon name="clipboard"/></button>
          <button className={`icon-button ${clipboard.pending ? 'clipboard-pending' : ''}`} title="Nhận clipboard từ đối tác" aria-label="Nhận clipboard từ đối tác" disabled={!clipboard.pending} onClick={() => clipboard.receiveFromRemote().then(ok => {if(ok) setNotice('Đã nhận clipboard từ đối tác');})}><Icon name="clipboard"/><span className="clipboard-arrow">↓</span></button>
        </>}
        {connected && <button className="disconnect compact" onClick={disconnect} disabled={busy}>Ngắt kết nối</button>}
      </div>
    </header>
    {(error || remote.status.error) && <div className="error-banner" role="alert"><span>{error || remote.status.error}</span>{error && <button className="icon-button" aria-label="Đóng thông báo lỗi" onClick={() => setError('')}><Icon name="close" size={16}/></button>}</div>}
    <main className="workspace">
      <div className={`screen-stage ${display.fit ? 'fit' : 'actual'}`}>
        <canvas ref={remote.canvas} tabIndex={connected && !display.viewOnly ? 0 : -1} aria-label="Màn hình máy tính từ xa" className={remote.screenSize ? '' : 'hidden'}/>
        {!remote.screenSize && <div className="screen-empty"><Icon name="monitor" size={44}/><p>{active ? 'Đang chờ màn hình đối tác…' : 'Nhập ID và mật khẩu để kết nối'}</p></div>}
      </div>
    </main>
    {clipboard.error && connected && settings?.clipboard_sync && <div className="clipboard-notice" role="status"><span>{clipboard.error}</span>{clipboard.pending && <button className="secondary compact" onClick={clipboard.receiveFromRemote}>Nhận clipboard</button>}<button className="icon-button" aria-label="Đóng thông báo clipboard" onClick={clipboard.clearError}><Icon name="close" size={16}/></button></div>}
    {notice && <div className="toast" role="status"><Icon name="check" size={18}/>{notice}</div>}
    <div className="floating-tools"><Communication hidden={!chatOpen} status={remote.status} connected={connected} refresh={remote.refresh} onClose={() => setChatOpen(false)}/><button className={`chat-toggle ${chatOpen ? 'opened' : ''}`} onClick={() => setChatOpen(!chatOpen)} aria-label={chatOpen ? 'Ẩn chat và gửi file' : 'Hiện chat và gửi file'} title={chatOpen ? 'Ẩn chat và gửi file' : 'Hiện chat và gửi file'} aria-expanded={chatOpen}><Icon name="chevron" size={18}/>{!chatOpen && unread > 0 && <span className="unread">{unread}</span>}</button></div>
    {settingsOpen && <Settings settings={settings} display={display} connecting={remote.status.state === 'connecting'} onClose={() => setSettingsOpen(false)} onSave={saveSettings}/>}
  </div>;
}
