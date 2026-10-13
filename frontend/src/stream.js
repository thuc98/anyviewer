import { createControl } from './control.js';
import { errorMessage } from './errors.js';
import { bootstrap, post, streamSocket } from './api.js';

export function openScreenStream(mode, onFrame, onState, settings = {}, controls = {}) {
  let stopped = false, socket, peer, channel, timeout, retry, video, videoCallback, statsTimer;
  let control, controlSocket, videoTimeout;
  let videoConnected=false, videoDecoded=false, hasSource=false;
  let active = mode, chunks = [], size = 0, generation = 0;
  const decoder = new TextDecoder();
  const streamID=globalThis.crypto?.randomUUID?.() || `s${Date.now().toString(36)}${Math.random().toString(36).slice(2)}`;
  let stage='initialization';
  function diagnostic(event,reason='') {
    const details={stream_id:streamID,event,stage,reason:String(reason).slice(0,512),from:active,to:event==='fallback' ? (active==='webrtc'?'websocket':'polling') : active,encoding:settings.stream_encoding || 'jpeg',connection_state:peer?.connectionState || '',ice_state:peer?.iceConnectionState || '',signaling_state:peer?.signalingState || ''};
    if(event==='fallback') console.warn('Luồng hình ảnh chuyển sang chế độ dự phòng',details);
    else console.info('Đã hiển thị khung hình đầu tiên',details);
    post('/stream/log',details).catch(()=>{});
  }
  function closeTransport() {
    generation++; clearTimeout(timeout);clearTimeout(videoTimeout);videoTimeout=undefined;videoConnected=false;videoDecoded=false;hasSource=false;clearInterval(statsTimer);
    if(control) {control.close();control=null;}
    if(controlSocket) {controlSocket.onclose=null;controlSocket.onerror=null;controlSocket.onopen=null;controlSocket.onmessage=null;controlSocket.close();controlSocket=null;}
    if(video) {if(video.cancelVideoFrameCallback && videoCallback!==undefined) video.cancelVideoFrameCallback(videoCallback);video.pause();video.srcObject=null;video=null;}
    if (socket) { socket.onclose = null; socket.onerror = null; socket.onopen = null; socket.onmessage = null; socket.close(); socket = null; }
    if (peer) { peer.onconnectionstatechange = null; peer.close(); peer = null; }
    if (channel) { channel.onclose = null; channel.onopen = null; channel.onmessage = null; }
    channel = null; chunks = []; size = 0;
  }
  function sourceStatus(state) {
    hasSource=!!state.frames;
    if(!hasSource || videoDecoded) {clearTimeout(videoTimeout);videoTimeout=undefined;return;}
    if(active==='webrtc' && settings.stream_encoding==='vp8' && videoConnected && !videoTimeout) {
      videoTimeout=setTimeout(()=>{stage='first_video_frame';failed('video decoder did not render a frame after the remote screen became available');},10000);
    }
  }
  function bindControl(target) {
    control=createControl(text=>{
      const open=target instanceof WebSocket ? target.readyState===WebSocket.OPEN : target.readyState==='open';
      if(!open || target.bufferedAmount>65536) throw new Error('Kênh điều khiển không khả dụng.');
      target.send(text);
    },state=>controls.onStatus?.(state),ready=>controls.onReady?.(ready),()=>{target.onclose=null;target.close();});
    const bound=control;
    target.onopen=()=>bound.open();
    target.onmessage=event=>bound.receive(event.data);
    target.onclose=()=>bound.close();
    target.onerror=()=>bound.close();
  }
  function failed(reason) {
    if (stopped) return;
    diagnostic('fallback',reason);
    closeTransport();
    if (active === 'webrtc') {
      active = 'websocket'; onState(active, `WebRTC không khả dụng: ${errorMessage(reason)} Đang dùng WebSocket.`); connect();
    } else {
      onState('polling', `WebSocket không khả dụng: ${errorMessage(reason)} Đang cập nhật định kỳ; thử kết nối lại sau 5 giây.`);
      retry = setTimeout(() => { if (!stopped) { active = mode; connect(); } }, 5000);
    }
  }
  async function render(buffer, acknowledge, attempt) {
    try {
      if (buffer.byteLength < 4) throw new Error('Invalid frame');
      const length = new DataView(buffer).getUint32(0);
      if (length > 1024 || length + 4 >= buffer.byteLength) throw new Error('Invalid frame header');
      const header = JSON.parse(decoder.decode(new Uint8Array(buffer, 4, length)));
      const bitmap = await createImageBitmap(new Blob([new Uint8Array(buffer, 4 + length)], {type:`image/${header.encoding}`}));
      try { if (!stopped && attempt === generation) onFrame(header, bitmap, buffer.byteLength); } finally { bitmap.close(); }
      if (!stopped && attempt === generation) acknowledge();
    } catch (error) { if (attempt === generation) failed(error.diagnostic || error.message); }
  }
  async function connect() {
    const attempt = ++generation;
    try {
      await bootstrap();
      if (stopped || attempt !== generation) return;
      timeout = setTimeout(() => failed('connection timed out'), 15000);
      if (active === 'websocket') {
        stage='websocket_connect';
        socket = await streamSocket(streamID);
        if (stopped) { closeTransport(); return; }
        socket.binaryType = 'arraybuffer';
        if(controls.onStatus) {
          controlSocket=await streamSocket(streamID,'/stream/control');
          if(stopped || attempt!==generation) {controlSocket.close();return;}
          bindControl(controlSocket);
        }
        socket.onopen = () => { clearTimeout(timeout); onState('websocket', ''); };
        socket.onmessage = event => {
          const target = socket;
          render(event.data, () => { if (target?.readyState === WebSocket.OPEN) target.send('ack'); }, attempt);
        };
        socket.onerror = () => failed('connection error');
        socket.onclose = () => failed('connection closed');
      } else {
        stage='browser_support';
        if (!window.RTCPeerConnection) throw new Error('browser does not support WebRTC');
        const target = peer = new RTCPeerConnection();
        const isVideo = settings.stream_encoding === 'vp8';
        if (isVideo) {
          stage='create_video_receiver';
          target.addTransceiver('video', {direction:'recvonly'});
          target.ontrack = event => {
            if(stopped || peer!==target || event.track.kind!=='video') return;
            const player = video = document.createElement('video');
            player.muted=true;player.playsInline=true;player.autoplay=true;
            player.onerror=()=>{if(peer===target) {stage='video_decode';failed(`MediaError ${player.error?.code}: ${player.error?.message || 'video playback failed'}`);}};
            stage='video_playback';
            player.srcObject=event.streams[0] || new MediaStream([event.track]);
            let width=0,height=0,lastTime=-1,reported=false;
            function draw() {
              if(stopped || peer!==target || video!==player) return;
              if(player.readyState>=2 && player.currentTime!==lastTime) {
                const full=width!==player.videoWidth || height!==player.videoHeight;
                width=player.videoWidth;height=player.videoHeight;lastTime=player.currentTime;
                try {onFrame({width,height,x:0,y:0,full,encoding:'vp8'},player,0);} catch(error) {failed(error.diagnostic || error.message);return;}
                if(!reported) {reported=true;videoDecoded=true;clearTimeout(videoTimeout);videoTimeout=undefined;diagnostic('first_frame');clearTimeout(timeout);onState('webrtc','');}
              }
              if(player.requestVideoFrameCallback) videoCallback=player.requestVideoFrameCallback(draw);
              else requestAnimationFrame(draw);
            }
            player.play().then(draw).catch(error => {if(peer===target) failed(error.diagnostic || error.message);});
          };
        }
        if(controls.onStatus) bindControl(target.createDataChannel('control',{ordered:true}));
        channel = target.createDataChannel('screen', {ordered:true});
        const data = channel;
        data.binaryType = 'arraybuffer';
        data.onopen = () => {
          clearTimeout(timeout);
          if(isVideo) {videoConnected=true;sourceStatus({frames:hasSource?1:0});}
          if(!isVideo) {clearTimeout(timeout);onState('webrtc','');}
          else {
            let previousLost=0,previousReceived=0;
            statsTimer=setInterval(async()=>{
              try {
                const stats=await target.getStats();
                if(stopped || peer!==target || data.readyState!=='open') return;
                let rtt=0,loss=0,jitter=0;
                stats.forEach(report=>{
                  if(report.type==='candidate-pair' && report.state==='succeeded' && report.nominated) rtt=(report.currentRoundTripTime || 0)*1000;
                  if(report.type==='inbound-rtp' && report.kind==='video') {
                    const lost=Math.max(0,(report.packetsLost || 0)-previousLost),received=Math.max(0,(report.packetsReceived || 0)-previousReceived);
                    loss=lost/Math.max(1,lost+received);jitter=(report.jitter || 0)*1000;
                    previousLost=report.packetsLost || 0;previousReceived=report.packetsReceived || 0;
                  }
                });
                data.send(JSON.stringify({type:'feedback',rtt_ms:rtt,loss,jitter_ms:jitter}));
              } catch { /* Connection failure is handled by the peer state listener. */ }
            },1000);
          }
        };
        data.onmessage = event => {
          const chunk = new Uint8Array(event.data);
          if (!chunk.length || size + chunk.length - 1 > 128*1024*1024) { failed('invalid frame chunks'); return; }
          chunks.push(chunk.subarray(1)); size += chunk.length - 1;
          if (chunk[0] === 1) {
            const packet = new Uint8Array(size); let offset = 0;
            for (const part of chunks) { packet.set(part, offset); offset += part.length; }
            chunks = []; size = 0;
            render(packet.buffer, () => { if (data.readyState === 'open') data.send('ack'); }, attempt);
          }
        };
        data.onclose = () => { if (peer === target) failed('data channel closed'); };
        target.onconnectionstatechange = () => {
          if (['failed','disconnected','closed'].includes(target.connectionState)) {stage='peer_connection';failed(target.connectionState);}
        };
        stage='create_offer';
        await target.setLocalDescription(await target.createOffer());
        stage='ice_gathering';
        if (target.iceGatheringState !== 'complete') {
          await new Promise((resolve, reject) => {
            const timer = setTimeout(() => reject(new Error('ICE gathering timed out')), 10000);
            target.onicegatheringstatechange = () => {
              if (target.iceGatheringState === 'complete') { clearTimeout(timer); resolve(); }
            };
          });
        }
        if (stopped || peer !== target) return;
        stage='server_negotiation';
        const answer = await post(`/stream/rtc?stream_id=${encodeURIComponent(streamID)}`, target.localDescription);
        if (stopped || peer !== target) return;
        stage='set_remote_description';
        await target.setRemoteDescription(answer);
        stage='wait_for_video_or_channel';
      }
    } catch (error) { if (!stopped && attempt === generation) failed(error.diagnostic || error.message); }
  }
  onState(mode, ''); connect();
  const close=()=>{stopped=true;clearTimeout(retry);closeTransport();};
  close.sourceStatus=sourceStatus;
  close.input=value=>control?.input(value) || Promise.reject(new Error('Kênh điều khiển chưa sẵn sàng.'));
  close.refresh=()=>control?.refresh() || false;
  Object.defineProperty(close,'controlReady',{get:()=>!!control?.ready});
  return close;
}
