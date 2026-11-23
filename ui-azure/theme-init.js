(()=>{
    'use strict';
    let saved;
    try { saved = localStorage.getItem('deviy-theme'); } catch (_) {}
    const dark = saved === 'dark' || (saved !== 'light' && window.matchMedia?.('(prefers-color-scheme: dark)').matches);
    const root = document.documentElement;
    root.dataset.theme = dark ? 'dark' : 'light';
    root.style.colorScheme = dark ? 'dark' : 'light';
    root.style.backgroundColor = dark ? '#101827' : '#f5f7fb';
})();
