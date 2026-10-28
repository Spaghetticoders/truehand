const it = navigator.language.toLowerCase().startsWith('it')
const T = it
  ? { starting: 'Avvio di Truehand…', failed: 'Truehand non è partito.', retry: 'Riprova' }
  : { starting: 'Starting Truehand…', failed: 'Truehand did not start.', retry: 'Try again' }
const msg = document.getElementById('msg')
const detail = document.getElementById('detail')
const retry = document.getElementById('retry')
retry.textContent = T.retry

async function start() {
  msg.textContent = T.starting
  detail.hidden = retry.hidden = true
  try {
    const url = await window.__TAURI__.core.invoke('start')
    location.replace(url)
  } catch (e) {
    msg.textContent = T.failed
    detail.textContent = String(e)
    detail.hidden = retry.hidden = false
  }
}
retry.addEventListener('click', start)
start()
