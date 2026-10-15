import test from 'node:test';
import assert from 'node:assert/strict';
import {changedRegion} from './sharing-frame.js';

test('first frame and widespread changes require a full image',()=>{
  const pixels=new Uint32Array(65*49);
  assert.deepEqual(changedRegion(pixels,null,65,49),{x:0,y:0,width:65,height:49,full:true});
  assert.deepEqual(changedRegion(pixels,new Uint32Array(pixels.length).fill(1),65,49),{x:0,y:0,width:65,height:49,full:true});
});
test('unchanged frames are skipped and edge tiles stay inside the image',()=>{
  const previous=new Uint32Array(65*49),current=previous.slice();
  assert.equal(changedRegion(current,previous,65,49),null);
  current[65*49-1]=42;
  assert.deepEqual(changedRegion(current,previous,65,49),{x:64,y:32,width:1,height:17,full:false});
});
test('separate dirty tiles are included in one bounding region',()=>{
  const previous=new Uint32Array(256*256),current=previous.slice();
  current[34*256+34]=1;current[70*256+70]=1;
  assert.deepEqual(changedRegion(current,previous,256,256),{x:32,y:32,width:64,height:64,full:false});
});
