import { errorMessage } from './errors.js';

export function createControl(send, onStatus, onReady, terminate = () => {}) {
  let ready=false, closed=false, sequence=0, heartbeat, timer;
  const pending=new Map();
  function close() {
    if(closed) return;closed=true;
    ready=false;clearInterval(heartbeat);clearInterval(timer);
    for(const value of pending.values()) value.reject(new Error('Kênh điều khiển đã đóng.'));
    pending.clear();onReady(false);terminate();
  }
  return {
    open() {
      closed=false;ready=true;onReady(true);
      const ping=()=>{try {send(JSON.stringify({type:'heartbeat'}));} catch {close();}};
      ping();heartbeat=setInterval(ping,15000);
      timer=setInterval(()=>{if([...pending.values()].some(value=>Date.now()-value.started>5000)) close();},1000);
    },
    receive(text) {
      let message;try {message=JSON.parse(text);} catch {return;}
      if(message.type==='status') onStatus(message.status);
      if(message.type==='input_ack') {
        const value=pending.get(message.id);if(!value) return;
        pending.delete(message.id);
        if(message.error) value.reject(new Error(errorMessage(message.error)));else value.resolve();
      }
    },
    input(event) {
      if(!ready) return Promise.reject(new Error('Kênh điều khiển chưa sẵn sàng.'));
      if(pending.size>=64) return Promise.reject(new Error('Kênh điều khiển đang quá tải.'));
      const id=++sequence;
      return new Promise((resolve,reject)=>{
        pending.set(id,{resolve,reject,started:Date.now()});
        try {send(JSON.stringify({type:'input',id,event}));} catch(error) {pending.delete(id);reject(error);close();}
      });
    },
    refresh() {if(!ready) return false;try {send(JSON.stringify({type:'status_request'}));return true;} catch {close();return false;}},
    get ready(){return ready;},
    close,
  };
}
