import {changedRegion} from './sharing-frame.js';

let canvas,context,patch,patchContext,previous,lastFull=0;
self.onmessage=async({data})=>{
  const {bitmap,width,height,quality}=data;
  try {
    if(!canvas){canvas=new OffscreenCanvas(width,height);context=canvas.getContext('2d',{alpha:false,willReadFrequently:true});patch=new OffscreenCanvas(1,1);patchContext=patch.getContext('2d',{alpha:false});}
    const scale=Math.min(width/bitmap.width,height/bitmap.height);
    const drawWidth=Math.round(bitmap.width*scale),drawHeight=Math.round(bitmap.height*scale);
    context.fillStyle='#000';context.fillRect(0,0,width,height);
    context.drawImage(bitmap,Math.floor((width-drawWidth)/2),Math.floor((height-drawHeight)/2),drawWidth,drawHeight);
    const pixels=context.getImageData(0,0,width,height);
    const current=new Uint32Array(pixels.data.buffer);
    const forceFull=performance.now()-lastFull>=5000;
    const region=changedRegion(current,forceFull?null:previous,width,height);
    if(!region){self.postMessage({empty:true});return;}
    patch.width=region.width;patch.height=region.height;
    patchContext.setTransform(1,0,0,-1,0,region.height);
    patchContext.drawImage(canvas,region.x,region.y,region.width,region.height,0,0,region.width,region.height);
    let blob;
    for(let q=quality;q>=.35;q-=.1){blob=await patch.convertToBlob({type:'image/jpeg',quality:q});if(blob.size<1024*1024-256) break;}
    if(blob.size>=1024*1024-256) throw new Error('Khung hình vượt quá dung lượng truyền cho phép. Hãy giảm độ phân giải.');
    previous=current;
    if(region.full) lastFull=performance.now();
    self.postMessage({blob,metadata:{width,height,x:region.x,y:region.y,full:region.full,encoding:'native-jpeg'}});
  }catch(error){self.postMessage({error:error.message});}
  finally {bitmap.close();}
};
