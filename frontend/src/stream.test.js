import assert from 'node:assert/strict';
import test from 'node:test';

globalThis.document = {querySelector:() => null};
globalThis.window = {location:{href:'http://127.0.0.1:8765/'},addEventListener:() => {}};
globalThis.location = {protocol:'http:'};
globalThis.fetch = async () => ({ok:true,json:async () => ({token:'token',session:'session',type:'answer',sdp:'answer'})});
globalThis.createImageBitmap = async () => ({width:2,height:2,close(){this.closed=true;}});
const sockets = [];
class FakeSocket {
  static OPEN=1;
  constructor(url,protocols) {this.url=url;this.protocols=protocols;this.readyState=1;this.sent=[];sockets.push(this);setImmediate(()=>this.onopen?.());}
  send(value) {this.sent.push(value);}
  close() {this.readyState=3;}
}
globalThis.WebSocket=FakeSocket;
const {openScreenStream}=await import('./stream.js');
const settle=()=>new Promise(resolve=>setImmediate(resolve));
function frame(full=true,x=0,y=0) {
  const header=new TextEncoder().encode(JSON.stringify({width:2,height:2,x,y,full,encoding:'png'}));
  const packet=new Uint8Array(4+header.length+1);
  new DataView(packet.buffer).setUint32(0,header.length);packet.set(header,4);packet[packet.length-1]=1;
  return packet;
}

test('WebSocket renders full/dirty frames, acknowledges, and ignores late frames after cleanup',async()=>{
  const rendered=[],states=[];
  const stop=openScreenStream('websocket',(header,bitmap)=>rendered.push({header,bitmap}),(mode,error)=>states.push({mode,error}));
  await settle();await settle();
  const socket=sockets.at(-1);
  assert.deepEqual(socket.protocols,['viewer','token','session']);assert.ok(socket.url.searchParams.has('stream_id'));assert.equal(socket.url.searchParams.has('token'),false);
  socket.onmessage({data:frame().buffer});await settle();
  socket.onmessage({data:frame(false,1,1).buffer});await settle();
  assert.equal(rendered.length,2);assert.equal(rendered[1].header.full,false);
  assert.equal(rendered[1].header.x,1);assert.equal(rendered[1].bitmap.closed,true);
  assert.deepEqual(socket.sent,['ack','ack']);assert.equal(states.at(-1).mode,'websocket');
  const late=socket.onmessage;stop();late({data:frame().buffer});await settle();
  assert.equal(rendered.length,2);assert.equal(socket.readyState,3);
});

test('WebRTC reassembles ordered chunks and renders before acknowledgement',async()=>{
  let peer;
  window.RTCPeerConnection=globalThis.RTCPeerConnection=class {
    constructor(){peer=this;this.iceGatheringState='complete';}
    createDataChannel(){return this.channel={readyState:'open',sent:[],send(value){this.sent.push(value);}};}
    async createOffer(){return {type:'offer',sdp:'offer'};}
    async setLocalDescription(offer){this.localDescription=offer;}
    async setRemoteDescription(){this.channel.onopen();}
    close(){this.closed=true;}
  };
  const rendered=[],states=[];
  const stop=openScreenStream('webrtc',header=>rendered.push(header),mode=>states.push(mode));
  await settle();await settle();
  const packet=frame();
  const first=new Uint8Array(11);first.set(packet.subarray(0,10),1);
  const last=new Uint8Array(packet.length-10+1);last[0]=1;last.set(packet.subarray(10),1);
  peer.channel.onmessage({data:first.buffer});assert.equal(rendered.length,0);
  peer.channel.onmessage({data:last.buffer});await settle();
  assert.equal(rendered.length,1);assert.deepEqual(peer.channel.sent,['ack']);assert.equal(states.at(-1),'webrtc');
  stop();assert.equal(peer.closed,true);
});

test('Unavailable WebRTC falls back to WebSocket and failed WebSocket enables polling',async()=>{
  delete window.RTCPeerConnection;
  const states=[];
  const stop=openScreenStream('webrtc',()=>{},(mode,error)=>states.push({mode,error}));
  await settle();await settle();
  assert.ok(states.some(state=>state.mode==='websocket' && state.error.includes('WebRTC')));
  const socket=sockets.at(-1);socket.onerror();
  assert.equal(states.at(-1).mode,'polling');assert.ok(states.at(-1).error.includes('WebSocket'));
  stop();
});

test('VP8 receives video without camera access, renders on decoded frames, and releases playback',async()=>{
  let peer,player;
  document.createElement=tag=>{
    assert.equal(tag,'video');
    return player={readyState:2,currentTime:0,videoWidth:640,videoHeight:360,play:async()=>{},pause(){this.paused=true;},requestVideoFrameCallback(fn){this.callback=fn;return 1;},cancelVideoFrameCallback(){this.canceled=true;}};
  };
  window.RTCPeerConnection=globalThis.RTCPeerConnection=class {
    constructor(){peer=this;this.iceGatheringState='complete';}
    addTransceiver(kind,options){this.transceiver={kind,...options};}
    createDataChannel(){return this.channel={readyState:'open',send(){}};}
    async createOffer(){return {type:'offer',sdp:'video-offer'};}
    async setLocalDescription(offer){this.localDescription=offer;}
    async setRemoteDescription(){this.channel.onopen();this.ontrack({track:{kind:'video'},streams:[{id:'screen'}]});}
    close(){this.closed=true;}
  };
  const rendered=[];
  const stop=openScreenStream('webrtc',header=>rendered.push(header),()=>{},{stream_encoding:'vp8'});
  await settle();await settle();
  assert.deepEqual(peer.transceiver,{kind:'video',direction:'recvonly'});
  assert.equal(player.muted,true);assert.equal(rendered[0].encoding,'vp8');assert.equal(rendered[0].full,true);
  player.currentTime=0.033;player.callback();assert.equal(rendered[1].full,false);
  player.videoWidth=800;player.currentTime=0.066;player.callback();assert.equal(rendered[2].full,true);
  stop();assert.equal(player.paused,true);assert.equal(player.srcObject,null);assert.equal(player.canceled,true);
});


test('User-facing errors use Vietnamese while preserving actionable details',async()=>{
  const {errorMessage}=await import('./errors.js');
  assert.match(errorMessage('VP8 video requires FFmpeg with libvpx on the backend'),/Máy chủ.*FFmpeg/);
  assert.match(errorMessage('stream quality must be between 1 and 100'),/Chất lượng JPEG.*1.*100/);
  assert.match(errorMessage('Sai mật khẩu (Wrong Password)'),/^Sai mật khẩu/);
  assert.equal(errorMessage('Đã xảy ra lỗi kết nối.'),'Đã xảy ra lỗi kết nối.');
  assert.match(errorMessage(new Error('unexpected internal failure')),/nhật ký máy chủ/);
});

test('An idle VP8 connection clears negotiation timeout and waits for a source before starting the video watchdog',async()=>{
  const originalSetTimeout=globalThis.setTimeout,originalClearTimeout=globalThis.clearTimeout;
  const timers=new Map();let sequence=0,peer;
  globalThis.setTimeout=(fn,ms)=>{const id=++sequence;timers.set(id,{fn,ms});return id;};
  globalThis.clearTimeout=id=>timers.delete(id);
  window.RTCPeerConnection=globalThis.RTCPeerConnection=class {
    constructor(){peer=this;this.iceGatheringState='complete';}
    addTransceiver(){}
    createDataChannel(){return this.channel={readyState:'open',send(){}};}
    async createOffer(){return {type:'offer',sdp:'offer'};}
    async setLocalDescription(offer){this.localDescription=offer;}
    async setRemoteDescription(){this.channel.onopen();}
    close(){}
  };
  let stop;
  try {
    stop=openScreenStream('webrtc',()=>{},()=>{},{stream_encoding:'vp8'});
    await settle();await settle();
    assert.equal([...timers.values()].filter(timer=>timer.ms===15000).length,0);
    stop.sourceStatus({frames:0});assert.equal(timers.size,0);
    stop.sourceStatus({frames:1});assert.equal([...timers.values()].filter(timer=>timer.ms===10000).length,1);
    stop.sourceStatus({frames:0});assert.equal(timers.size,0);
  } finally {stop?.();globalThis.setTimeout=originalSetTimeout;globalThis.clearTimeout=originalClearTimeout;}
});
