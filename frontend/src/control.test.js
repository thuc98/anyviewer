import assert from 'node:assert/strict';
import test from 'node:test';
import {createControl} from './control.js';

test('Control sends heartbeat, receives status, and preserves input order',async()=>{
 const sent=[],states=[],availability=[];
 const control=createControl(text=>sent.push(JSON.parse(text)),value=>states.push(value),ready=>availability.push(ready));
 control.open();assert.equal(sent[0].type,'heartbeat');assert.equal(control.ready,true);
 control.receive(JSON.stringify({type:'status',status:{state:'connected'}}));assert.equal(states[0].state,'connected');
 const down=control.input({type:'key',vk:65,down:true});const up=control.input({type:'key',vk:65,down:false});
 assert.deepEqual(sent.slice(1).map(message=>message.event.down),[true,false]);
 control.receive(JSON.stringify({type:'input_ack',id:sent[1].id}));control.receive(JSON.stringify({type:'input_ack',id:sent[2].id}));
 await Promise.all([down,up]);assert.equal(control.refresh(),true);assert.equal(sent.at(-1).type,'status_request');
 control.close();assert.equal(control.ready,false);assert.deepEqual(availability,[true,false]);
});

test('Control close rejects unacknowledged keys and does not replay them',async()=>{
 const sent=[];const control=createControl(text=>sent.push(JSON.parse(text)),()=>{},()=>{});control.open();
 const pending=control.input({type:'button',button:'left',down:true});
 const rejected=assert.rejects(pending,/đã đóng/);control.close();await rejected;
 assert.equal(sent.filter(message=>message.type==='input').length,1);
});
