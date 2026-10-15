import assert from 'node:assert/strict';
import test from 'node:test';
import {startBrowserShare,browserShareCapabilities} from './sharing.js';

function captureEnvironment(t) {
  const track=new EventTarget();track.readyState='live';track.stop=()=>{track.stopped=true;};
  const audio={stop(){this.stopped=true;}};
  let audioTracks=[audio];
  const stream={getTracks:()=>[track,...audioTracks],getVideoTracks:()=>[track],getAudioTracks:()=>audioTracks,removeTrack(value){audioTracks=audioTracks.filter(track=>track!==value);}};
  const calls={draws:0};
  const video={videoWidth:3840,videoHeight:2160,play:async()=>{},pause(){this.paused=true;}};
  const canvas={getContext:()=>({fillRect(){},drawImage(){calls.draws++;}}),toBlob(callback,type,quality){calls.type=type;calls.quality=quality;callback(new Blob(['image'],{type}));}};
  const oldNavigator=Object.getOwnPropertyDescriptor(globalThis,'navigator');
  const oldDocument=Object.getOwnPropertyDescriptor(globalThis,'document');
  Object.defineProperty(globalThis,'navigator',{configurable:true,value:{mediaDevices:{getDisplayMedia:async options=>{calls.options=options;return stream;}}}});
  Object.defineProperty(globalThis,'document',{configurable:true,value:{createElement:tag=>tag==='video'?video:canvas}});
  t.after(()=>{
    if(oldNavigator) Object.defineProperty(globalThis,'navigator',oldNavigator);else delete globalThis.navigator;
    if(oldDocument) Object.defineProperty(globalThis,'document',oldDocument);else delete globalThis.document;
  });
  return {track,audio,video,canvas,calls};
}

test('Browser sharing sends only images and waits for frame delivery',async t=>{
  const env=captureEnvironment(t);
  let delivered,release;
  const firstFrame=new Promise(resolve=>{delivered=resolve;});
  const acknowledgement=new Promise(resolve=>{release=resolve;});
  let stopped=0;
  const share=await startBrowserShare({fps:60,sendFrame:async(frame,size)=>{delivered({frame,size});await acknowledgement;},onStop:()=>stopped++});
  t.after(()=>share.stop());
  const {frame,size}=await firstFrame;
  assert.equal(env.calls.options.audio,false);
  assert.deepEqual(env.calls.options.video.frameRate,{ideal:60});
  assert.equal(env.audio.stopped,true);
  assert.deepEqual(share.capabilities,{screen:true,keyboard:false,mouse:false,clipboard:false});
  assert.equal(Object.isFrozen(browserShareCapabilities),true);
  assert.equal(frame.type,'image/jpeg');
  assert.deepEqual(size,{width:1920,height:1080});
  await new Promise(resolve=>setTimeout(resolve,40));
  assert.equal(env.calls.draws,1,'waiting for delivery must not accumulate frames');
  env.track.dispatchEvent(new Event('ended'));
  release();
  await new Promise(resolve=>setTimeout(resolve,25));
  assert.equal(env.calls.draws,1);
  assert.equal(env.track.stopped,true);
  assert.equal(env.video.srcObject,null);
  assert.equal(env.canvas.width,0);
  share.stop();assert.equal(stopped,1);
});

test('Browser sharing stops capture when transport fails',async t=>{
  const env=captureEnvironment(t);
  let failed;
  const failure=new Promise(resolve=>{failed=resolve;});
  const share=await startBrowserShare({sendFrame:async()=>{throw new Error('connection lost');},onError:failed});
  t.after(()=>share.stop());
  const error=await failure;
  assert.match(error.message,/connection lost/);
  assert.equal(env.track.stopped,true);
  assert.equal(env.video.paused,true);
});

test('Browser sharing releases capture if preview startup fails',async t=>{
  const env=captureEnvironment(t);
  env.video.play=async()=>{throw new Error('preview failed');};
  await assert.rejects(startBrowserShare({sendFrame:async()=>{}}),/preview failed/);
  assert.equal(env.track.stopped,true);
  assert.equal(env.video.srcObject,null);
  await assert.rejects(startBrowserShare({fps:Infinity,sendFrame:async()=>{}}),/không hợp lệ/);
});

test('Duplicate decoded video frames skip encoding and send a lightweight heartbeat',async t=>{
  const env=captureEnvironment(t);
  env.video.getVideoPlaybackQuality=()=>({totalVideoFrames:1});
  const original=Object.getOwnPropertyDescriptor(globalThis,'performance');
  let now=0;
  Object.defineProperty(globalThis,'performance',{configurable:true,value:{now:()=>now}});
  t.after(()=>Object.defineProperty(globalThis,'performance',original));
  let first,heartbeat;
  const initial=new Promise(resolve=>{first=resolve;});
  const idle=new Promise(resolve=>{heartbeat=resolve;});
  const share=await startBrowserShare({fps:60,sendFrame:async(frame,size)=>{if(frame)first();else heartbeat(size);}});
  t.after(()=>share.stop());
  await initial;
  await new Promise(resolve=>setTimeout(resolve,40));
  assert.equal(env.calls.draws,1);
  now=6000;
  const size=await idle;
  assert.deepEqual(size,{width:1920,height:1080,heartbeat:true});
  assert.equal(env.calls.draws,1,'heartbeat must not encode another full image');
  share.stop();
});

test('Background capture resizes the bitmap before transfer and releases its worker',async t=>{
  captureEnvironment(t);
  const saved=['Worker','OffscreenCanvas','createImageBitmap'].map(name=>[name,Object.getOwnPropertyDescriptor(globalThis,name)]);
  t.after(()=>{for(const [name,value] of saved){if(value)Object.defineProperty(globalThis,name,value);else delete globalThis[name];}});
  let options,terminated=false,transferred;
  class Worker {
    postMessage(data,transfer){transferred=transfer;queueMicrotask(()=>this.onmessage({data:{blob:new Blob(['jpeg']),metadata:{width:data.width,height:data.height,encoding:'native-jpeg'}}}));}
    terminate(){terminated=true;}
  }
  Object.defineProperty(globalThis,'Worker',{configurable:true,value:Worker});
  Object.defineProperty(globalThis,'OffscreenCanvas',{configurable:true,value:class {}});
  const bitmap={close(){}};
  Object.defineProperty(globalThis,'createImageBitmap',{configurable:true,value:async(video,value)=>{options=value;return bitmap;}});
  let first;
  const ready=new Promise(resolve=>{first=resolve;});
  const share=await startBrowserShare({nativeFrames:true,sendFrame:async()=>first()});
  t.after(()=>share.stop());
  await ready;
  assert.deepEqual(options,{resizeWidth:1920,resizeHeight:1080});
  assert.deepEqual(transferred,[bitmap]);
  share.stop();assert.equal(terminated,true);
});
