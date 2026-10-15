export const browserShareCapabilities = Object.freeze({screen:true, keyboard:false, mouse:false, clipboard:false});

export async function startBrowserShare({sendFrame, onStop=()=>{}, onError=()=>{}, fps=30, quality=.92, maxWidth=1920, maxHeight=1080, nativeFrames=false}) {
  if(typeof sendFrame!=='function') throw new Error('Thiếu kênh gửi hình ảnh.');
  if(!navigator.mediaDevices?.getDisplayMedia) throw new Error('Trình duyệt không hỗ trợ chia sẻ màn hình. Hãy dùng HTTPS hoặc localhost.');
  if(!Number.isFinite(fps) || fps<1 || fps>60 || !Number.isFinite(quality) || quality<=0 || quality>1 || !Number.isInteger(maxWidth) || !Number.isInteger(maxHeight) || maxWidth<1 || maxHeight<1 || maxWidth>8192 || maxHeight>8192 || maxWidth*maxHeight>16*1024*1024) throw new Error('Cài đặt chia sẻ hình ảnh không hợp lệ.');
  const stream=await navigator.mediaDevices.getDisplayMedia({video:{frameRate:{ideal:fps}},audio:false});
  const video=document.createElement('video');
  const canvas=document.createElement('canvas');
  video.muted=true;video.playsInline=true;
  let closed=false,timer,frameSize,worker,work,lastVideoFrame,lastDelivery=performance.now();
  function stop() {
    if(closed) return;
    closed=true;clearTimeout(timer);
    worker?.terminate();if(work){work.resolve({empty:true});work=null;}
    for(const track of stream.getTracks()) {track.removeEventListener('ended',stop);track.stop();}
    video.pause();video.srcObject=null;
    canvas.width=0;canvas.height=0;
    onStop();
  }
  try {
    for(const track of stream.getAudioTracks()) {stream.removeTrack(track);track.stop();}
    const track=stream.getVideoTracks()[0];
    if(!track || track.readyState==='ended') throw new Error('Không có màn hình để chia sẻ.');
    track.addEventListener('ended',stop);
    video.srcObject=stream;
    await video.play();
    if(closed) return {stop,capabilities:browserShareCapabilities};
    const context=canvas.getContext('2d',{alpha:false});
    if(!context) throw new Error('Không thể xử lý hình ảnh chia sẻ.');
    if(nativeFrames && typeof Worker!=='undefined' && typeof OffscreenCanvas!=='undefined' && typeof createImageBitmap!=='undefined') {
      worker=new Worker(new URL('./sharing-worker.js',import.meta.url),{type:'module'});
      worker.onmessage=({data})=>{const pending=work;work=null;if(!pending)return;if(data.error) pending.reject(new Error(data.error));else pending.resolve(data);};
      worker.onerror=()=>{work?.reject(new Error('Không thể xử lý hình ảnh trong luồng nền.'));work=null;};
    }
    async function capture() {
      const started=performance.now();
      try {
        if(closed) return;
        if(video.videoWidth && video.videoHeight) {
          const scale=Math.min(1,maxWidth/video.videoWidth,maxHeight/video.videoHeight);
          if(!frameSize) frameSize={width:Math.max(1,Math.round(video.videoWidth*scale)),height:Math.max(1,Math.round(video.videoHeight*scale))};
          const {width,height}=frameSize;
          const decoded=video.getVideoPlaybackQuality?.().totalVideoFrames;
          const duplicate=decoded>0 && decoded===lastVideoFrame;
          lastVideoFrame=decoded;
          if(duplicate){
            if(performance.now()-lastDelivery>=5000){await sendFrame(null,{width,height,heartbeat:true});lastDelivery=performance.now();}
          } else if(worker){
            const bitmapScale=Math.min(1,width/video.videoWidth,height/video.videoHeight);
            const bitmap=await createImageBitmap(video,{resizeWidth:Math.max(1,Math.round(video.videoWidth*bitmapScale)),resizeHeight:Math.max(1,Math.round(video.videoHeight*bitmapScale))});
            if(closed){bitmap.close();return;}
            const result=await new Promise((resolve,reject)=>{work={resolve,reject};worker.postMessage({bitmap,width,height,quality},[bitmap]);});
            if(closed)return;
            if(!result.empty){await sendFrame(result.blob,result.metadata);lastDelivery=performance.now();}
            else if(performance.now()-lastDelivery>=5000){await sendFrame(null,{width,height,heartbeat:true});lastDelivery=performance.now();}
          } else {
          if(canvas.width!==width || canvas.height!==height) {canvas.width=width;canvas.height=height;}
          const fitted=Math.min(width/video.videoWidth,height/video.videoHeight);
          const drawWidth=Math.round(video.videoWidth*fitted),drawHeight=Math.round(video.videoHeight*fitted);
          context.fillStyle='#000';context.fillRect(0,0,width,height);
          context.drawImage(video,Math.floor((width-drawWidth)/2),Math.floor((height-drawHeight)/2),drawWidth,drawHeight);
          const frame=await new Promise(resolve=>canvas.toBlob(resolve,'image/jpeg',quality));
          if(closed) return;
          if(!frame) throw new Error('Không thể mã hóa hình ảnh chia sẻ.');
          await sendFrame(frame,{width,height});lastDelivery=performance.now();
          }
        }
        if(!closed) timer=setTimeout(capture,Math.max(0,1000/fps-(performance.now()-started)));
      } catch(error) {
        if(closed) return;
        stop();onError(error);
      }
    }
    timer=setTimeout(capture,0);
    return {stop,stream,capabilities:browserShareCapabilities};
  } catch(error) {
    stop();throw error;
  }
}
