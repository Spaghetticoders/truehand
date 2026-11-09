// Phone layout below this width; above it the app stays exactly as on desktop.
const query = window.matchMedia('(max-width: 640px)')

export const layout = $state({ phone: query.matches })

query.addEventListener('change', (e) => (layout.phone = e.matches))
