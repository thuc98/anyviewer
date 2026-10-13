import { useEffect, useRef, useState } from 'react';
import { getClipboard, post } from './api';
import { errorMessage } from './errors';

export function useClipboard(connected, enabled, viewOnly) {
  const [error, setError] = useState('');
  const [pending, setPending] = useState(false);
  const remote = useRef({ version:0, text:'' });
  const lastText = useRef('');
  const applied = useRef(0);
  const canRead = useRef(false);
  const sending = useRef(null);
  const active = useRef(false);
  const allowSend = useRef(!viewOnly);
  allowSend.current = !viewOnly;
  active.current = connected && enabled;

  async function sendFromLocal({ quiet = false } = {}) {
    if (!active.current || viewOnly) return false;
    if (sending.current) return sending.current;
    sending.current = (async () => {
      try {
        if (!navigator.clipboard?.readText) throw new Error('Trình duyệt chưa hỗ trợ bộ nhớ tạm. Dùng localhost hoặc HTTPS.');
        const text = await navigator.clipboard.readText();
        if (!active.current) return false;
        canRead.current = true;
        if (!text) { if(!quiet) setError('bộ nhớ tạm không có văn bản.'); return false; }
        if (new TextEncoder().encode(text).length > 128*1024 || text.length*2 > 64*1024) throw new Error('bộ nhớ tạm vượt quá 64 KB văn bản.');
        if (text !== lastText.current) { await post('/clipboard',{ text }); lastText.current = text; }
        setError(''); return true;
      } catch (e) {
        canRead.current = false;
        setError(e.name === 'NotAllowedError' ? 'Cho phép bộ nhớ tạm hoặc dùng nút Gửi/Nhận bộ nhớ tạm.' : errorMessage(e));
        return false;
      } finally { sending.current = null; }
    })();
    return sending.current;
  }
  async function receiveFromRemote() {
    if (!active.current || !remote.current.version) return false;
    try {
      if (!navigator.clipboard?.writeText) throw new Error('Trình duyệt chưa hỗ trợ bộ nhớ tạm. Dùng localhost hoặc HTTPS.');
      const latest = remote.current;
      await navigator.clipboard.writeText(latest.text);
      lastText.current = latest.text; applied.current = latest.version;
      setPending(false); setError(''); return true;
    } catch (e) {
      setPending(true);
      setError(e.name === 'NotAllowedError' ? 'Nhấn Nhận bộ nhớ tạm để sao chép văn bản từ đối tác.' : errorMessage(e));
      return false;
    }
  }
  const actions = useRef({ sendFromLocal, receiveFromRemote });
  actions.current = { sendFromLocal, receiveFromRemote };
  useEffect(() => {
    let stopped = false, timer;
    const abort = new AbortController();
    remote.current = {version:0,text:''}; applied.current = 0; lastText.current = ''; canRead.current = false;
    setPending(false); setError('');
    if (!connected || !enabled) return;
    navigator.permissions?.query({name:'clipboard-read'}).then(permission => { if (!stopped) canRead.current=permission.state === 'granted'; }).catch(() => {});
    async function tick() {
      if (stopped) return;
      try {
        if (!document.hidden && document.hasFocus()) {
          const value = await getClipboard(AbortSignal.any([abort.signal,AbortSignal.timeout(3000)]));
          if (stopped) return;
          if (value.error) setError(errorMessage(value.error));
          if (value.version && value.version !== remote.current.version) { remote.current=value; setPending(true); }
          if (remote.current.version && remote.current.version !== applied.current) {

            if (remote.current.attempted !== true) { remote.current.attempted=true; await actions.current.receiveFromRemote(); }
          }

          if (allowSend.current && canRead.current && (!remote.current.version || applied.current === remote.current.version)) await actions.current.sendFromLocal({quiet:true});
        }
      } catch (e) { if (!stopped && e.name !== 'AbortError') setError('Không thể đồng bộ bộ nhớ tạm.'); }
      finally { if (!stopped) timer=setTimeout(tick,1000); }
    }
    tick();
    return () => { stopped=true; abort.abort(); clearTimeout(timer); };
  }, [connected, enabled]);
  return { sendFromLocal, receiveFromRemote, pending, error, clearError:() => setError('') };
}
