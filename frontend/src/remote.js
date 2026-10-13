import { useCallback, useEffect, useRef, useState } from 'react';
import { post, request } from './api';
import { openScreenStream } from './stream';

const special = { Enter:13, Escape:27, Backspace:8, Tab:9, Space:32, ShiftLeft:160, ShiftRight:161, ControlLeft:162, ControlRight:163, AltLeft:164, AltRight:165, MetaLeft:91, MetaRight:92, ArrowLeft:37, ArrowUp:38, ArrowRight:39, ArrowDown:40, Home:36, End:35, PageUp:33, PageDown:34, Delete:46, Insert:45, CapsLock:20, Semicolon:186, Equal:187, Comma:188, Minus:189, Period:190, Slash:191, Backquote:192, BracketLeft:219, Backslash:220, BracketRight:221, Quote:222 };
export function keycode(code) {
  if (special[code] !== undefined) return special[code];
  if (/^Key[A-Z]$/.test(code)) return code.charCodeAt(3);
  if (/^Digit[0-9]$/.test(code)) return code.charCodeAt(5);
  if (/^F([1-9]|1[0-2])$/.test(code)) return 111 + Number(code.slice(1));
  return null;
}

export function useRemote(interval, viewOnly, beforePaste, settings) {
  const canvas = useRef(null);
  const connection = useRef(null);
  const controlReady = useRef(false);
  const [ready,setReady]=useState(false);
  const applyStatus=useCallback(state=>{connection.current?.sourceStatus(state);setStatus(state);setReady(true);setOnline(true);if(!state.frames) setScreenSize(null);},[]);
  const mode = settings?.stream_transport || "polling";
  const [transport, setTransport] = useState("polling");
  const [streamError, setStreamError] = useState("");
  const actualTransport = useRef("polling");
  useEffect(() => {
    if (mode === "polling") { actualTransport.current="polling";setTransport("polling");setStreamError("");return; }
    const stream=openScreenStream(mode, (header, bitmap) => {
      const target=canvas.current; if (!target) return;
      if(header.full) { target.width=header.width;target.height=header.height; }
      if(target.width!==header.width || target.height!==header.height) throw new Error("Screen patch needs a full frame");
      target.getContext("2d").drawImage(bitmap,header.x,header.y);
      setScreenSize(previous => previous?.width===header.width && previous?.height===header.height ? previous : {width:header.width,height:header.height});
    }, (active,error) => { actualTransport.current=active;setTransport(active);setStreamError(error); }, settings, {
      onStatus:applyStatus,
      onReady:available=>{controlReady.current=available;wake.current();},
    });
    connection.current=stream;
    return ()=>{connection.current=null;stream();};
  }, [mode, settings?.stream_encoding, settings?.stream_quality, settings?.stream_fps, settings?.stream_dirty, settings?.stream_adaptive, settings?.stream_bitrate]);
  const [status, setStatus] = useState({ state: 'disconnected', frames: 0 });
  const [online, setOnline] = useState(true);
  const [screenSize, setScreenSize] = useState(null);
  const timer = useRef();
  const wake = useRef(() => {});
  const inputs = useRef(Promise.resolve());
  const held = useRef(new Set());
  const buttons = useRef(new Set());
  const dispatchInput=useCallback(value=>connection.current?.controlReady ? connection.current.input(value) : post('/event',value),[]);
  const pendingMove=useRef(null);
  const moveScheduled=useRef(false);
  const queueInput=useCallback(value=>{
    inputs.current=inputs.current.then(()=>dispatchInput(value)).catch(()=>{});
  },[dispatchInput]);
  const flushMove=useCallback(()=>{if(pendingMove.current) {const value=pendingMove.current;pendingMove.current=null;queueInput(value);}},[queueInput]);
  const sendInput=useCallback(value=>{
    if(value.type!=='move') {flushMove();queueInput(value);return;}
    pendingMove.current=value;
    if(moveScheduled.current) return;
    moveScheduled.current=true;
    inputs.current.finally(()=>{
      moveScheduled.current=false;flushMove();
    });
  },[queueInput,flushMove]);
  const release = useCallback(() => {
    for (const vk of held.current) sendInput({ type:'key', vk, down:false });
    for (const button of buttons.current) sendInput({ type:'button', button, down:false });
    held.current.clear(); buttons.current.clear();
  }, [sendInput]);
  useEffect(() => {
    let stopped = false, busy = false, lastFrame = 0, pending = false;
    const abort = new AbortController();
    async function refresh() {
      if (stopped) return;
      if (busy) { pending = true; return; }
      busy = true; let delay = 2000;
      if(controlReady.current) {busy=false;return;}
      try {
        const response = await request('/status', { cache:'no-store', signal:AbortSignal.any([abort.signal, AbortSignal.timeout(3000)]) });
        if (!response.ok) throw new Error('Status unavailable');
        const state = await response.json();
        if (stopped) return;
        applyStatus(state);
        delay = state.state === 'disconnected' ? 2000 : actualTransport.current === 'polling' ? interval : 1000;
        if (!state.frames) { lastFrame = 0; setScreenSize(null); }
        if (actualTransport.current === "polling" && state.frames && state.frames !== lastFrame && !document.hidden) {
          const frame = await request('/frame.png', { cache:'no-store', signal:AbortSignal.any([abort.signal, AbortSignal.timeout(3000)]) });
          if (frame.status !== 204 && frame.ok) {
            const bitmap = await createImageBitmap(await frame.blob());
            if (!stopped && canvas.current) {
              const target = canvas.current;
              target.width = bitmap.width; target.height = bitmap.height;
              target.getContext('2d').drawImage(bitmap, 0, 0);
              setScreenSize(previous => previous?.width===bitmap.width && previous?.height===bitmap.height ? previous : {width:bitmap.width,height:bitmap.height}); lastFrame = state.frames;
            }
            bitmap.close();
          }
        }
      } catch { if (!stopped) {setOnline(false);setReady(true);} }
      finally {
        busy = false;
        if (!stopped) { timer.current = setTimeout(refresh, pending ? 0 : document.hidden ? 5000 : delay); pending = false; }
      }
    }
    wake.current = () => { clearTimeout(timer.current); if(!connection.current?.refresh()) refresh(); };
    document.addEventListener('visibilitychange', wake.current);
    refresh();
    return () => { stopped = true; abort.abort(); clearTimeout(timer.current); document.removeEventListener('visibilitychange', wake.current); };
  }, [interval]);
  useEffect(() => { window.addEventListener('blur', release); return () => { window.removeEventListener('blur', release); release(); }; }, [release]);
  useEffect(() => { if (viewOnly || status.state !== 'connected' || !online) release(); }, [viewOnly, status.state, online, release]);
  useEffect(() => {
    const target = canvas.current;
    let lastMove = 0;
    const enabled = () => status.state === 'connected' && online && !viewOnly;
    const point = event => {
      const r = target.getBoundingClientRect();
      return { x:Math.max(0,Math.min(target.width-1,Math.floor((event.clientX-r.left)*target.width/r.width))), y:Math.max(0,Math.min(target.height-1,Math.floor((event.clientY-r.top)*target.height/r.height))) };
    };
    target.onpointermove = event => { if (!enabled() || performance.now()-lastMove<35) return; lastMove=performance.now(); sendInput({type:'move', ...point(event)}); };
    target.onpointerdown = event => {
      if (!enabled()) return; event.preventDefault(); target.focus(); target.setPointerCapture(event.pointerId);
      const button=['left','middle','right'][event.button];
      if (button) { sendInput({type:'move', ...point(event)}); sendInput({type:'button',button,down:true}); buttons.current.add(button); }
    };
    target.onpointerup = event => { const button=['left','middle','right'][event.button]; if (button && buttons.current.has(button)) { sendInput({type:'button',button,down:false}); buttons.current.delete(button); } };
    target.onpointercancel = release; target.onblur = release; target.oncontextmenu = event => event.preventDefault();
    target.onkeydown = event => {
      const vk=beforePaste?.current && event.code.startsWith("Meta") ? (event.code === "MetaRight" ? 163 : 162) : keycode(event.code); if (!enabled() || vk===null) return;
      event.preventDefault(); held.current.add(vk);
      if (vk===86 && (event.ctrlKey || event.metaKey) && beforePaste?.current && !event.repeat) {
        const prepared=beforePaste.current();
        inputs.current=inputs.current.then(async()=>{if(await prepared) await dispatchInput({type:'key',vk,down:true});}).catch(()=>{});
      } else sendInput({type:'key',vk,down:true});
    };
    target.onkeyup = event => { const vk=beforePaste?.current && event.code.startsWith('Meta') ? (event.code === 'MetaRight' ? 163 : 162) : keycode(event.code); if (vk===null || !held.current.has(vk)) return; event.preventDefault(); held.current.delete(vk); sendInput({type:'key',vk,down:false}); };
    const wheel = event => { if (!enabled()) return; event.preventDefault(); sendInput({type:'wheel',delta:event.deltaY>0?-120:120}); };
    target.addEventListener('wheel', wheel, {passive:false});
    return () => { target.removeEventListener('wheel',wheel); release(); };
  }, [status.state, online, viewOnly, release, sendInput, beforePaste, dispatchInput]);
  async function disconnect() { release(); await post('/disconnect'); wake.current(); }
  return { canvas, ready, status, online, screenSize, transport, streamError, disconnect, refresh:() => wake.current() };
}
