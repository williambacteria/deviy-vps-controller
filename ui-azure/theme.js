(()=>{
    'use strict';
    const root = document.documentElement;
    const storageKey = 'deviy-theme';
    const system = window.matchMedia('(prefers-color-scheme: dark)');
    const moon = '<svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 13.1A9 9 0 0 1 10.9 3 9 9 0 1 0 21 13.1Z"/></svg>';
    const sun = '<svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M2 12h2M20 12h2M5 5l1.4 1.4m11.2 11.2L19 19M5 19l1.4-1.4M17.6 6.4 19 5"/></svg>';
    const hexPattern = /#([0-9a-f]{6}|[0-9a-f]{3})(?![0-9a-f])/gi;
    const rgbPattern = /rgba?\(\s*([\d.]+)[, ]+([\d.]+)[, ]+([\d.]+)(?:\s*[,/]\s*([\d.]+))?\s*\)/gi;
    const normalize = h => h.length === 3 ? h.split('').map(c => c+c).join('') : h;
    // Keep server-rendered and asynchronously refreshed inline colors in the same palette.
    function normalizeInline(element) {
        if (!(element instanceof HTMLElement) || !element.hasAttribute('style')) return;
        const style = element.style;
        for (const name of [...style]) {
            const role = name === 'color' ? 't' : /^(background|background-color)$/.test(name) ? 'b' : /^(border(-.*)?|outline(-.*)?)$/.test(name) ? 'e' : null;
            if (!role) continue;
            const value = style.getPropertyValue(name);
            if (value.includes('var(')) continue;
            const mapColor = (full,h) => {
                const token='--dt-'+role+'-'+normalize(h.toLowerCase());
                return getComputedStyle(root).getPropertyValue(token) ? 'var('+token+','+full+')' : full;
            };
            const changed = value.replace(rgbPattern, (full,r,g,b,a) => {
                if (a!==undefined && +a<1) return full;
                return mapColor(full,[r,g,b].map(x=>Math.round(+x).toString(16).padStart(2,'0')).join(''));
            }).replace(hexPattern, (full,h,offset,whole) => {
                if (whole.slice(0,offset).lastIndexOf('var(')>whole.slice(0,offset).lastIndexOf(')')) return full;
                return mapColor(full,h);
            });
            if (changed !== value) style.setProperty(name, changed, style.getPropertyPriority(name));
        }
    }
    function normalizeTree(node) {
        if (!(node instanceof Element)) return;
        normalizeInline(node);
        node.querySelectorAll('[style]').forEach(normalizeInline);
    }
    function updateLogos() {
        document.querySelectorAll('img[src*="devicloud-logo"],img[src*="/__azure/logo"]').forEach(img => {
            if (!img.dataset.themeLightSrc) img.dataset.themeLightSrc = img.getAttribute('src').replace('-light.png','.png');
            const light = img.dataset.themeLightSrc;
            const dark = light.includes('/__azure/') ? light.replace('logo.png','logo-light.png') : light.replace('devicloud-logo.png','devicloud-logo-light.png');
            const next = root.dataset.theme === 'dark' ? dark : light;
            if (img.getAttribute('src') !== next) img.setAttribute('src', next);
        });
    }
    function updateButtons() {
        const dark = root.dataset.theme === 'dark';
        document.querySelectorAll('[data-theme-toggle]').forEach(button => {
            button.innerHTML=(dark?sun:moon)+'<span>'+(dark?'明亮模式':'黑夜模式')+'</span>';
            button.setAttribute('aria-label',dark?'切换为明亮模式':'切换为黑夜模式');
            button.setAttribute('title',dark?'切换为明亮模式':'切换为黑夜模式');
            button.setAttribute('aria-checked',String(dark));
        });
    }
    function apply(theme, persist) {
        const dark = theme === 'dark';
        root.dataset.theme = dark ? 'dark' : 'light';
        root.style.colorScheme = dark ? 'dark' : 'light';
        root.style.backgroundColor = dark ? '#101827' : '#f5f7fb';
        if (persist) { try { localStorage.setItem(storageKey, root.dataset.theme); } catch (_) {} }
        updateButtons();updateLogos();
        window.dispatchEvent(new CustomEvent('deviy:themechange',{detail:{theme:root.dataset.theme}}));
    }
    function init() {
        // Controller navigation is emitted by its existing display adapter.
        if (!document.querySelector('[data-theme-toggle]')) {
            const target = document.querySelector('.az-topbar') || document.querySelector('#login-form');
            if (target) {
                const button = document.createElement('button');
                button.type='button';button.className='theme-toggle';button.dataset.themeToggle='';button.setAttribute('role','switch');
                if (target.id === 'login-form') {
                    const holder=document.createElement('div');holder.className='theme-fixed';holder.append(button);document.body.append(holder);
                } else target.append(button);
            }
        }
        normalizeTree(document.body);updateButtons();updateLogos();
        document.addEventListener('click',event => {
            if (event.target.closest('[data-theme-toggle]')) apply(root.dataset.theme==='dark'?'light':'dark',true);
        });
        const observer=new MutationObserver(records => {
            let logos=false;
            for (const record of records) {
                if (record.type==='attributes') normalizeInline(record.target);
                else for (const node of record.addedNodes) {
                    if (!(node instanceof Element)) continue;
                    normalizeTree(node);
                    if (node.matches('img') || node.querySelector('img')) logos=true;
                }
            }
            if (logos) updateLogos();
        });
        observer.observe(document.body,{childList:true,subtree:true,attributes:true,attributeFilter:['style']});
    }
    window.addEventListener('storage',event => {
        if (event.key===storageKey) apply(event.newValue==='dark'?'dark':event.newValue==='light'?'light':system.matches?'dark':'light',false);
    });
    system.addEventListener('change',event => {
        let saved;try { saved=localStorage.getItem(storageKey); } catch (_) {}
        if (!saved) apply(event.matches?'dark':'light',false);
    });
    if (document.readyState==='loading') document.addEventListener('DOMContentLoaded',init,{once:true}); else init();
})();
