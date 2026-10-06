'use strict';
const $ = id => document.getElementById(id);
const state = {nodes: [], selected: null, samples: [], historyError: '', historyID: 0, pollSeconds: 5, loadingHistory: false, historyNode: null};
const number = (v, digits=1) => Number(v).toLocaleString('zh-CN', {maximumFractionDigits:digits});
function bytes(v) { if (!Number.isFinite(v)) return '—'; const units=['B','KiB','MiB','GiB','TiB']; let i=0;while(v>=1024&&i<units.length-1){v/=1024;i++;}return `${number(v,v<10?1:0)} ${units[i]}`; }
const pct = v => `${number(v)}%`;
const clock = v => new Date(v).toLocaleTimeString('zh-CN',{hour:'2-digit',minute:'2-digit',second:'2-digit'});
function uptime(v){const d=Math.floor(v/86400),h=Math.floor(v%86400/3600),m=Math.floor(v%3600/60);return d?`${d} 天 ${h} 小时`:`${h} 小时 ${m} 分钟`;}
function text(id,value){$(id).textContent=value;}
async function api(path,options={}){const response=await fetch(path,{...options,headers:{'Content-Type':'application/json',...options.headers},signal:options.signal||AbortSignal.timeout(15000)});if(response.status===204)return null;let data;try{data=await response.json();}catch{throw new Error(`请求失败 (HTTP ${response.status})`);}if(!response.ok)throw new Error(data.error||`HTTP ${response.status}`);return data;}
function el(tag,className,value){const e=document.createElement(tag);if(className)e.className=className;if(value!==undefined)e.textContent=value;return e;}
function meter(value,online){const cell=el('td','',online?pct(value):'—');const bar=el('div',`meter ${value>85?'warning':''}`);const fill=el('span');fill.style.width=`${Math.max(0,Math.min(100,online?value:0))}%`;bar.append(fill);cell.append(bar);return cell;}
function render(){
 const online=state.nodes.filter(n=>n.status==='online'&&n.snapshot);
 text('nav-count',state.nodes.length);text('fleet-count',state.nodes.length);text('online-count',`${online.length} / ${state.nodes.length}`);
 text('online-note',state.nodes.length?`${state.nodes.length-online.length} 个节点离线或连接中`:'添加节点即可开始');
 text('average-cpu',online.length?pct(online.reduce((s,n)=>s+n.snapshot.cpu_percent,0)/online.length):'—');
 const mem=online.reduce((s,n)=>s+n.snapshot.memory.used,0),total=online.reduce((s,n)=>s+n.snapshot.memory.total,0);
 text('memory-total',online.length?bytes(mem):'—');text('memory-note',online.length?`共 ${bytes(total)} · ${pct(mem/total*100)}`:'在线节点合计');
 text('network-total',online.length?`${bytes(online.reduce((s,n)=>s+n.snapshot.network.receive_bytes_per_second+n.snapshot.network.transmit_bytes_per_second,0))}/s`:'—');
 const query=$('search').value.trim().toLowerCase();const filtered=state.nodes.filter(n=>`${n.name} ${n.ssh||''} ${n.url}`.toLowerCase().includes(query));
 const rows=$('node-rows');rows.replaceChildren();
 for(const node of filtered){const tr=el('tr',node.id===state.selected?'selected':'');tr.tabIndex=0;tr.setAttribute('aria-label',`查看 ${node.name}`);tr.addEventListener('click',()=>selectNode(node.id));tr.addEventListener('keydown',e=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();selectNode(node.id);}});
 const name=el('td');name.append(el('div','node-name',node.name),el('div','node-address',node.ssh?`SSH · ${node.ssh}`:node.url));
 const status=el('td');status.append(el('span',`badge ${node.status}`,{online:'在线',offline:'离线',connecting:'连接中'}[node.status]||node.status));if(node.error)status.title=node.error;
 const s=node.snapshot,up=node.status==='online'&&s;tr.append(name,status,meter(s?.cpu_percent||0,up),meter(s?.memory.percent||0,up),meter(s?.disk.percent||0,up));
 const net=el('td','net-value');if(up){net.append(el('div','',`↓ ${bytes(s.network.receive_bytes_per_second)}/s`),el('div','',`↑ ${bytes(s.network.transmit_bytes_per_second)}/s`));}else net.textContent='—';tr.append(net,el('td','muted',up?`${node.latency_ms} ms`:'—'));rows.append(tr);}
 $('empty').hidden=state.nodes.length>0;$('no-results').hidden=state.nodes.length===0||filtered.length>0;document.querySelector('.table-scroll').hidden=state.nodes.length===0;
 text('fleet-note',`每 ${state.pollSeconds} 秒更新 · ${clock(Date.now())}`);renderDetail();
}
function selectNode(id){if(state.selected===id)return;state.selected=id;state.samples=[];state.historyError='';render();loadHistory();}
function renderDetail(){const n=state.nodes.find(n=>n.id===state.selected);$('detail').hidden=!n;if(!n)return;const s=n.snapshot;text('detail-name',n.name);text('detail-meta',`${n.ssh?`SSH · ${n.ssh} · `:''}${n.url}${s?` · ${s.hostname}`:''}`);
 const stale=n.status!=='online';$('node-error').hidden=!stale;text('node-error',`${n.error||'正在建立连接…'}${s?' · 下方详情为最后一次成功读取的数据。':''}`);
 text('detail-uptime',s?uptime(s.uptime_seconds):'—');text('detail-cores',s?`${s.cores} cores`:'—');text('detail-load',s?s.load.map(v=>number(v,2)).join(' / '):'—');text('detail-swap',s?`${bytes(s.swap.used)} / ${bytes(s.swap.total)}`:'—');text('detail-last',n.last_success?clock(n.last_success):'—');drawCharts();
}
async function refresh(){try{const result=await api('/api/nodes');state.nodes=result.nodes;state.pollSeconds=result.poll_seconds;$('global-error').hidden=true;const connection=$('connection');connection.className='connection connected';connection.textContent='客户端已连接';
 if(!state.nodes.some(n=>n.id===state.selected)){state.selected=state.nodes[0]?.id||null;state.samples=[];state.historyNode=null;}
 render();if(state.selected)await loadHistory();
 }catch(e){$('global-error').hidden=false;text('global-error',`无法连接客户端：${e.message}。画面保留最近一次读取的结果。`);$('connection').className='connection';text('connection','客户端连接中断');}}
async function loadHistory(){if(!state.selected)return;const id=state.selected,request=++state.historyID;state.loadingHistory=true;drawCharts();const since=new Date(Date.now()-Number($('range').value)*60000).toISOString();
 try{const data=await api(`/api/nodes/${encodeURIComponent(id)}/history?since=${encodeURIComponent(since)}&limit=360`);if(request!==state.historyID||id!==state.selected)return;state.samples=data.samples;state.historyError='';state.historyNode=id;state.loadingHistory=false;const retention=Math.round(data.retention_seconds/3600);text('history-note',`${data.samples.length} 个采样点 · 节点保留 ${retention} 小时 · ${clock(Date.now())} 更新`);drawCharts();}
 catch(e){if(request!==state.historyID||id!==state.selected)return;state.loadingHistory=false;state.historyError=e.message;text('history-note',`历史读取失败：${e.message}${state.samples.length?' · 保留此前曲线':''}`);drawCharts();}}
function draw(canvas,series,max,label){const rect=canvas.getBoundingClientRect();if(!rect.width)return;const ratio=window.devicePixelRatio||1;canvas.width=Math.round(rect.width*ratio);canvas.height=Math.round(rect.height*ratio);const ctx=canvas.getContext('2d');ctx.scale(ratio,ratio);const w=rect.width,h=rect.height,left=45,right=7,top=12,bottom=8;ctx.font='9px "Segoe UI", sans-serif';ctx.textBaseline='middle';
 for(let i=0;i<5;i++){const y=top+(h-top-bottom)*i/4;ctx.strokeStyle='#eaf0e8';ctx.beginPath();ctx.moveTo(left,y);ctx.lineTo(w-right,y);ctx.stroke();ctx.fillStyle='#98a497';ctx.fillText(label(max*(1-i/4)),0,y);}
 if(!state.samples.length){ctx.textAlign='center';ctx.fillStyle='#8d988f';ctx.font='11px "Segoe UI", "Microsoft YaHei", sans-serif';ctx.fillText(state.loadingHistory?'正在读取节点历史…':state.historyError?'历史暂不可用':'尚无历史采样，探针运行后将自动显示',(w+left)/2,h/2);return;}
 const first=new Date(state.samples[0].timestamp).getTime(),last=new Date(state.samples.at(-1).timestamp).getTime(),duration=Math.max(1,last-first);const gap=state.samples.length>1?Math.max(30000,duration/(state.samples.length-1)*4):30000;
 series.forEach(({value,color})=>{ctx.strokeStyle=color;ctx.lineWidth=1.8;ctx.beginPath();let previous=0;state.samples.forEach((sample,index)=>{const time=new Date(sample.timestamp).getTime(),x=left+(time-first)/duration*(w-left-right),y=top+(1-Math.min(1,Math.max(0,value(sample)/max)))*(h-top-bottom);if(index===0||time-previous>gap)ctx.moveTo(x,y);else ctx.lineTo(x,y);previous=time;});ctx.stroke();if(state.samples.length===1){const y=top+(1-Math.min(1,value(state.samples[0])/max))*(h-top-bottom);ctx.beginPath();ctx.arc(left,y,3,0,2*Math.PI);ctx.fillStyle=color;ctx.fill();}});
}
function drawCharts(){if($('detail').hidden)return;draw($('resource-chart'),[{value:s=>s.cpu_percent,color:'#22764c'},{value:s=>s.memory.percent,color:'#8174b4'}],100,v=>`${Math.round(v)}%`);const max=Math.max(1024,...state.samples.map(s=>Math.max(s.network.receive_bytes_per_second,s.network.transmit_bytes_per_second)))*1.15;draw($('network-chart'),[{value:s=>s.network.receive_bytes_per_second,color:'#22764c'},{value:s=>s.network.transmit_bytes_per_second,color:'#8174b4'}],max,v=>bytes(v));const start=state.samples.length?clock(state.samples[0].timestamp):'—',end=state.samples.length?clock(state.samples.at(-1).timestamp):'—';text('chart-start',start);text('network-start',start);text('chart-end',end);text('network-end',end);}
function openAdd(){$('form-error').hidden=true;$('node-dialog').showModal();}
$('add-node').addEventListener('click',openAdd);$('empty-add').addEventListener('click',openAdd);$('cancel-add').addEventListener('click',()=>$('node-dialog').close());$('search').addEventListener('input',render);$('range').addEventListener('change',()=>{state.samples=[];loadHistory();});
$('connect-mode').addEventListener('change',()=>{const ssh=$('connect-mode').value==='ssh';$('ssh-field').hidden=!ssh;text('url-help',ssh?'远端机器上的回环地址，由 SSH 隧道转发。':'远程节点使用 HTTPS；本机 HTTP 仅限回环地址。');});
$('node-form').addEventListener('submit',async e=>{e.preventDefault();const data=new FormData(e.currentTarget),ssh=data.get('mode')==='ssh'?String(data.get('ssh')).trim():'';if(data.get('mode')==='ssh'&&!ssh){$('form-error').hidden=false;text('form-error','请输入 SSH 主机别名。');return;}const node={name:String(data.get('name')).trim(),url:String(data.get('url')).trim(),token:String(data.get('token')).trim(),ssh};$('submit-add').disabled=true;
 try{const added=await api('/api/nodes',{method:'POST',body:JSON.stringify(node)});state.selected=added.id;state.samples=[];$('node-form').reset();$('ssh-field').hidden=false;text('url-help','远端机器上的回环地址，由 SSH 隧道转发。');$('node-dialog').close();await refresh();}catch(error){$('form-error').hidden=false;text('form-error',error.message);}finally{$('submit-add').disabled=false;}});
$('remove-node').addEventListener('click',()=>{const n=state.nodes.find(n=>n.id===state.selected);if(!n)return;text('remove-description',n.name);$('remove-error').hidden=true;$('remove-dialog').dataset.node=n.id;$('remove-dialog').showModal();});$('cancel-remove').addEventListener('click',()=>$('remove-dialog').close());
$('remove-form').addEventListener('submit',async e=>{e.preventDefault();$('confirm-remove').disabled=true;try{await api(`/api/nodes/${encodeURIComponent($('remove-dialog').dataset.node)}`,{method:'DELETE'});$('remove-dialog').close();await refresh();}catch(error){$('remove-error').hidden=false;text('remove-error',error.message);}finally{$('confirm-remove').disabled=false;}});
new ResizeObserver(drawCharts).observe($('detail'));window.addEventListener('resize',drawCharts);
async function loop(){await refresh();setTimeout(loop,state.pollSeconds*1000);}loop();
