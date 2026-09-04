// app.js — общий UI: навигация, модалки.
function navBar() {
  return {
    user: null,
    async init() {
      this.user = await guard();
      if (!this.user && !location.pathname.startsWith('/login')) {
        location.href = '/login';
        return;
      }
      Store.emit('user', this.user);
    },
    show(name) {
      if (!this.user) return false;
      if (['users', 'nodes'].includes(name)) return this.user.role === 'admin';
      return true;
    },
    active(path) {
      const p = location.pathname;
      if (path === '/dashboard') return p === '/' || p === '/dashboard';
      if (path === '/instances') return p.startsWith('/instances');
      return p.startsWith(path);
    },
    async logout() {
      try { await api('/api/auth/logout', { method: 'POST' }); } catch (e) { /* ignore */ }
      location.href = '/login';
    }
  };
}
