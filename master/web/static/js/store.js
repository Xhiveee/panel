// store.js — минимальный pub/sub стор для общего состояния UI.
window.Store = {
  _subs: {},
  on(event, fn) {
    (this._subs[event] = this._subs[event] || []).push(fn);
    return () => { this._subs[event] = this._subs[event].filter(f => f !== fn); };
  },
  emit(event, data) {
    (this._subs[event] || []).forEach(fn => {
      try { fn(data); } catch (e) { console.error(e); }
    });
  }
};
