export function changedRegion(current,previous,width,height,tileSize=32) {
  if(!previous || previous.length!==current.length) return {x:0,y:0,width,height,full:true};
  let left=width,top=height,right=0,bottom=0;
  for(let y=0;y<height;y+=tileSize) for(let x=0;x<width;x+=tileSize){
    const endX=Math.min(width,x+tileSize),endY=Math.min(height,y+tileSize);
    let changed=false;
    for(let row=y;row<endY && !changed;row++) for(let col=x;col<endX;col++) if(current[row*width+col]!==previous[row*width+col]){changed=true;break;}
    if(changed){left=Math.min(left,x);top=Math.min(top,y);right=Math.max(right,endX);bottom=Math.max(bottom,endY);}
  }
  if(right===0) return null;
  if((right-left)*(bottom-top)>=width*height*.75) return {x:0,y:0,width,height,full:true};
  return {x:left,y:top,width:right-left,height:bottom-top,full:false};
}
