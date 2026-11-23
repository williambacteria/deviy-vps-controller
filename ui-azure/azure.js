(()=>{
'use strict';
const icons={overview:'<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',hosts:'<rect x="3" y="3" width="18" height="7" rx="2"/><rect x="3" y="14" width="18" height="7" rx="2"/><path d="M7 6.5h.01M7 17.5h.01M16 6.5h2M16 17.5h2"/>',vms:'<rect x="3" y="3" width="18" height="13" rx="2"/><path d="M8 21h8M12 16v5M7 7h10M7 11h5"/>',images:'<path d="m12 3 9 5-9 5-9-5 9-5ZM3 12l9 5 9-5M3 17l9 5 9-5"/>',pools:'<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a18 18 0 0 1 0 18 18 18 0 0 1 0-18"/>',api:'<path d="m8 6-6 6 6 6M16 6l6 6-6 6M14 3l-4 18"/>',menu:'<path d="M4 6h16M4 12h16M4 18h16"/>',link:'<path d="M14 3h7v7M21 3 10 14M10 3H3v18h18v-7"/>'};
const svg=name=>'<svg class="az-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'+icons[name]+'</svg>';
const make=(tag,cls,html)=>{const e=document.createElement(tag);e.className=cls;if(html)e.innerHTML=html;return e};
// The upstream binary emits a redundant direct body text node before its shell.
for(const node of [...document.body.childNodes])if(node.nodeType===Node.TEXT_NODE&&/^(登录\s*·\s*)?Deviy Controller$/.test(node.textContent.trim()))node.textContent='';
const logo='/__azure/logo.png?v=20261124-brand1';
let brandIcon=document.querySelector('link[rel~="icon"]');
if(!brandIcon){brandIcon=document.createElement('link');brandIcon.rel='icon';document.head.append(brandIcon)}
brandIcon.href='/__azure/favicon.ico?v=20261124-brand1';
if(document.getElementById('login-form')){
 document.body.classList.add('az-login');document.title='管理员登录 · Deviy Controller';document.querySelectorAll('.brand-logo').forEach(e=>e.src=logo);
 const form=document.getElementById('login-form');const img=make('img','az-login-brand');img.src=logo;img.alt='Deviy Cloud 德威云计算';img.style.width='165px';img.style.height='auto';img.style.marginBottom='27px';form.prepend(img);return;
}
const side=document.querySelector('.side'),app=document.querySelector('.app');if(!side||!app)return;
document.body.classList.add('az-controller');document.querySelectorAll('.brand-logo').forEach(e=>e.src=logo);
const brand=side.querySelector('.brand');brand.after(make('div','az-workspace','<span class="az-workspace-icon">D</span><div><b>基础设施工作空间</b><small>INFRASTRUCTURE CONSOLE</small></div>'));
const nav=side.querySelector('.nav');nav.id='az-controller-nav';nav.before(make('div','az-nav-label','基础设施管理'));
nav.querySelectorAll('[data-view]').forEach(button=>button.insertAdjacentHTML('afterbegin',svg(button.dataset.view)));
const close=make('button','az-menu-close','×');close.type='button';close.setAttribute('aria-label','关闭导航');side.prepend(close);
const top=make('header','az-topbar','<button class="az-menu" type="button" aria-label="打开导航" aria-expanded="false" aria-controls="az-controller-nav">'+svg('menu')+'</button><div class="az-crumb"><span>基础设施</span><span>/</span><b>控制面板</b></div><span class="az-product">DEVIY CONTROLLER</span><a class="az-toplink" href="https://www.deviy.cn/admin" target="_blank" rel="noopener">运营后台 '+svg('link')+'</a>');
app.before(top);const backdrop=make('div','az-backdrop');document.body.append(backdrop);
const toggle=top.querySelector('.az-menu');let previousFocus;
function setNav(open){document.body.classList.toggle('az-nav-open',open);toggle.setAttribute('aria-expanded',String(open));toggle.setAttribute('aria-label',open?'关闭导航':'打开导航');if(open){previousFocus=document.activeElement;close.focus()}else if(previousFocus){previousFocus.focus();previousFocus=null}}
toggle.addEventListener('click',()=>setNav(!document.body.classList.contains('az-nav-open')));close.addEventListener('click',()=>setNav(false));backdrop.addEventListener('click',()=>setNav(false));
document.addEventListener('keydown',e=>{if(e.key==='Escape')setNav(false);if(e.key==='Tab'&&document.body.classList.contains('az-nav-open')){const items=[...side.querySelectorAll('button,a,input,select')].filter(el=>!el.disabled&&el.getClientRects().length);const first=items[0],last=items.at(-1);if(e.shiftKey&&document.activeElement===first){e.preventDefault();last.focus()}else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first.focus()}}});
window.addEventListener('resize',()=>{if(innerWidth>760)setNav(false)});
const title=document.getElementById('title');function syncTitle(){const text=location.pathname.startsWith('/vm/')?'实例详情':title?.textContent||'控制面板';top.querySelector('.az-crumb b').textContent=text;nav.querySelectorAll('button').forEach(b=>b.setAttribute('aria-current',b.classList.contains('active')?'page':'false'))}
if(title)new MutationObserver(syncTitle).observe(title,{childList:true,subtree:true,characterData:true});syncTitle();
const main=document.getElementById('app-main'),heading=main?.querySelector('.top>div');if(heading)heading.prepend(make('div','az-eyebrow','INFRASTRUCTURE OVERVIEW'));
const labels=['hosts','vms','images','pools'];document.querySelectorAll('#view-overview>.grid>.card').forEach((card,i)=>card.append(make('span','az-stat-icon',svg(labels[i]))));
document.querySelectorAll('.app>.main').forEach(el=>el.append(make('footer','az-footer','<span>© '+new Date().getFullYear()+' Deviy Cloud · 德威云计算</span><span>基础设施 · 资源编排 · 运维管理</span>')));
})();
