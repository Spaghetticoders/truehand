// Generates real WebAuthn vectors with Chromium's CDP virtual authenticator.
// usage: PW=<path to the playwright package> CHROME=<chromium binary> node gen-webauthn-vectors.js internal/sign/testdata
const { chromium } = require(process.env.PW);
const http = require('http');
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

const outDir = process.argv[2];
const b64u = (b) => Buffer.from(b).toString('base64url');

function intentMessage(i) {
  const keys = ['action', 'repo', 'task', 'plan_sha256', 'head', 'nonce', 'time', 'approver'];
  return 'truehand-intent/1\n' + keys.map((k) => `${k}: ${i[k] ?? ''}\n`).join('');
}
function serverMessage(i) {
  const keys = ['action', 'server', 'subject', 'nonce', 'time', 'credential'];
  return 'truehand-server-intent/1\n' + keys.map((k) => `${k}: ${i[k] ?? ''}\n`).join('');
}

(async () => {
  const srv = http.createServer((req, res) => {
    res.writeHead(200, { 'content-type': 'text/html' });
    res.end('<!doctype html><title>v</title>');
  });
  await new Promise((r) => srv.listen(0, '127.0.0.1', r));
  const port = srv.address().port;
  const origin = `http://localhost:${port}`;
  const browser = await chromium.launch({ executablePath: process.env.CHROME, headless: true });
  const page = await browser.newPage();
  await page.goto(origin + '/');
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('WebAuthn.enable');

  for (const [name, alg, attestation] of [['es256', -7, 'none'], ['rs256', -257, 'none'], ['es256-packed', -7, 'direct']]) {
    const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
      options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true },
    });
    const challenge = crypto.randomBytes(32);
    const reg = await page.evaluate(async ({ challenge, alg, attestation }) => {
      const u8 = (s) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
      const enc = (b) => btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
      const c = await navigator.credentials.create({ publicKey: {
        rp: { id: 'localhost', name: 'Truehand' },
        user: { id: new Uint8Array([1, 2, 3, 4]), name: 'alice', displayName: 'Alice' },
        challenge: u8(challenge), pubKeyCredParams: [{ type: 'public-key', alg }],
        authenticatorSelection: { userVerification: 'required', residentKey: 'preferred' }, attestation,
      } });
      return { id: enc(c.rawId), clientDataJSON: enc(c.response.clientDataJSON), attestationObject: enc(c.response.attestationObject), alg: c.response.getPublicKeyAlgorithm() };
    }, { challenge: challenge.toString('base64'), alg, attestation });

    const intent = {
      action: 'approve-plan', repo: 'github.com/spaghetticoders/demo', task: 'T-1',
      plan_sha256: crypto.createHash('sha256').update('plan').digest('hex'), head: '',
      nonce: crypto.randomBytes(16).toString('hex'), time: new Date().toISOString().replace(/\.\d+Z$/, 'Z'), approver: 'phone',
    };
    const serverIntent = {
      action: 'pair-code', server: 'localhost', subject: crypto.createHash('sha256').update('{}').digest('hex'),
      nonce: crypto.randomBytes(16).toString('hex'), time: intent.time, credential: reg.id,
    };
    const assertions = {};
    for (const [k, msg] of [['intent', intentMessage(intent)], ['server', serverMessage(serverIntent)]]) {
      const ch = crypto.createHash('sha256').update(msg).digest();
      assertions[k] = await page.evaluate(async ({ ch, id }) => {
        const u8 = (s) => Uint8Array.from(atob(s.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
        const enc = (b) => btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
        const a = await navigator.credentials.get({ publicKey: { challenge: u8(ch), rpId: 'localhost', allowCredentials: [{ type: 'public-key', id: u8(id) }], userVerification: 'required' } });
        return { authenticatorData: enc(a.response.authenticatorData), clientDataJSON: enc(a.response.clientDataJSON), signature: enc(a.response.signature) };
      }, { ch: ch.toString('base64url'), id: reg.id });
    }
    const v = { name, browser: browser.version(), rpID: 'localhost', origin, challenge: b64u(challenge), registration: reg, intent, serverIntent, assertions };
    fs.writeFileSync(path.join(outDir, `webauthn-${name}.json`), JSON.stringify(v, null, 2) + '\n');
    await cdp.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId });
    console.log(name, 'ok', reg.alg);
  }
  await browser.close();
  srv.close();
})().catch((e) => { console.error(e); process.exit(1); });
