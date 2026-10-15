import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import vm from 'node:vm';
import {changedRegion} from './sharing-frame.js';

test('sharing worker flips JPEG once, skips unchanged frames, without periodic full-image spikes',async()=>{
  const messages=[],transforms=[],encoded=[];
  let now=0,closed=0;
  class Canvas {
    constructor(width,height){this.width=width;this.height=height;}
    getContext(){return {fillRect(){},drawImage(){},getImageData:()=>({data:new Uint8ClampedArray(this.width*this.height*4)}),setTransform:(...args)=>transforms.push(args)};}
    async convertToBlob(options){encoded.push(options);return new Blob(['jpeg']);}
  }
  const self={postMessage:value=>messages.push(value)};
  const source=(await readFile(new URL('./sharing-worker.js',import.meta.url),'utf8')).replace(/^import[^\n]+\n/,'');
  vm.runInNewContext(source,{self,OffscreenCanvas:Canvas,Uint32Array,changedRegion,performance:{now:()=>now}});
  const send=()=>self.onmessage({data:{bitmap:{width:64,height:48,close(){closed++;}},width:64,height:48,quality:.92}});
  await send();
  assert.equal(messages[0].metadata.encoding,'native-jpeg');
  assert.equal(messages[0].metadata.full,true);
  assert.deepEqual(transforms[0],[1,0,0,-1,0,48]);
  assert.equal(encoded[0].quality,.92);
  await send();assert.equal(messages[1].empty,true);assert.equal(encoded.length,1);
  now=5001;await send();assert.equal(messages[2].empty,true);assert.equal(encoded.length,1);
  assert.equal(closed,3);
});
