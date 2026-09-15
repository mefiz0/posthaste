<script lang="ts">
  import Icon from './Icon.svelte';
  import { focusTrap } from '../lib/focus';
  import { overlayFade } from '../lib/transitions';
  import { app, addAccount, closeAccountSetup } from '../lib/stores.svelte';
  import { api, backendKind, openExternal } from '../lib/api';
  import type { DiscoveredConfig, ManualAccountInput, ServerConfig, ServerSecurity } from '../lib/types';

  type Step = 'email' | 'auth' | 'manual';

  let step = $state<Step>('email');
  let email = $state('');
  let discovered = $state<DiscoveredConfig | null>(null);
  let discovering = $state(false);
  let error = $state('');
  let password = $state('');
  let verifying = $state(false);
  let oauthWaiting = $state(false);

  let imapHost = $state('');
  let imapPort = $state(993);
  let imapSecurity = $state<ServerSecurity>('tls');
  let smtpHost = $state('');
  let smtpPort = $state(587);
  let smtpSecurity = $state<ServerSecurity>('starttls');
  let username = $state('');
  let oauthStateId = $state('');

  function reset(): void {
    step = 'email';
    email = '';
    discovered = null;
    discovering = false;
    error = '';
    password = '';
    verifying = false;
    oauthWaiting = false;
    oauthStateId = '';
  }

  $effect(() => {
    if (app.accountSetupOpen) reset();
  });

  function applyDiscovered(config: DiscoveredConfig): void {
    discovered = config;
    imapHost = config.imap.host;
    imapPort = config.imap.port;
    imapSecurity = config.imap.security;
    smtpHost = config.smtp.host;
    smtpPort = config.smtp.port;
    smtpSecurity = config.smtp.security;
    username = config.imap.username;
  }

  async function onDiscover(): Promise<void> {
    error = '';
    if (!isEmail(email)) {
      error = 'Enter a valid email address.';
      return;
    }
    discovering = true;
    try {
      const config = await api.discover(email.trim());
      if (!config) {
        error = 'No settings found for this address. Enter them manually below.';
        applyDiscovered({
          email,
          providerName: email.split('@')[1] ?? '',
          requiresOAuth: false,
          imap: { host: `imap.${(email.split('@')[1] ?? '').replace(/^@/, '')}`, port: 993, security: 'tls', username: email },
          smtp: { host: `smtp.${(email.split('@')[1] ?? '').replace(/^@/, '')}`, port: 587, security: 'starttls', username: email },
        });
        step = 'manual';
        return;
      }
      applyDiscovered(config);
      step = 'auth';
    } catch {
      error = 'Could not look up settings for this address.';
    } finally {
      discovering = false;
    }
  }

  function isEmail(value: string): boolean {
    return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim());
  }

  function currentInput(): ManualAccountInput {
    return {
      email: email.trim(),
      password: password,
      auth: discovered?.requiresOAuth ? 'oauth' : 'password',
      imap: { host: imapHost.trim(), port: Number(imapPort) || 993, security: imapSecurity, username: username.trim() },
      smtp: { host: smtpHost.trim(), port: Number(smtpPort) || 587, security: smtpSecurity, username: username.trim() },
    };
  }

  async function onVerify(): Promise<void> {
    error = '';
    verifying = true;
    try {
      const input = currentInput();
      await api.verifyAccount(input);
      await addAccount(input);
      closeAccountSetup();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Verification failed.';
    } finally {
      verifying = false;
    }
  }

  async function onOAuth(): Promise<void> {
    error = '';
    oauthWaiting = true;
    try {
      const { url, stateId, complete } = await api.startOAuth(email.trim());
      oauthStateId = stateId;
      // The engine's StartOAuth already opens the consent screen in the
      // default browser; only the mock needs the frontend to present the URL.
      if (backendKind === 'mock') openExternal(url);
      const result = await complete;
      if (!result.success) {
        error = result.error ?? 'Sign-in did not complete.';
        return;
      }
      await addAccount(currentInput());
      closeAccountSetup();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Sign-in failed.';
    } finally {
      oauthWaiting = false;
      oauthStateId = '';
    }
  }

  /**
   * Asks the engine to abandon the pending flow. The waiting promise settles
   * through the flow's own oauth-complete event, which keeps this component's
   * state and the engine's in step.
   */
  function onCancelOAuth(): void {
    if (!oauthStateId) return;
    void api.cancelOAuth(oauthStateId).catch(() => {});
  }

  const securityOptions: ServerSecurity[] = ['tls', 'starttls', 'none'];
</script>

<div class="overlay open" transition:overlayFade role="presentation">
  <button class="backdrop" aria-label="Close account setup" onclick={closeAccountSetup}></button>
  <div class="dialog pane" role="dialog" aria-modal="true" aria-label="Add account" use:focusTrap>
    <div class="pane-head">
      <h2>Add account</h2>
      <span class="spacer"></span>
      <button class="x" title="Close" aria-label="Close account setup" onclick={closeAccountSetup}>
        <Icon name="more" />
      </button>
    </div>

    {#if step === 'email'}
      <p class="note">Enter your email address — Posthaste looks up the server settings for you. Your mail stays between you and your provider.</p>
      <div class="field">
        <label for="setup-email">Email address</label>
        <!-- svelte-ignore a11y_autofocus -->
        <input
          id="setup-email"
          data-autofocus
          class="text-input"
          type="email"
          placeholder="you@example.com"
          spellcheck="false"
          bind:value={email}
          onkeydown={(event) => {
            if (event.key === 'Enter') void onDiscover();
          }}
        />
      </div>
      {#if error}<p class="form-error">{error}</p>{/if}
      <div class="pane-foot">
        <span class="grow"></span>
        <button class="btn" onclick={() => { error = ''; step = 'manual'; }}>Manual configuration</button>
        <button class="btn primary" disabled={discovering} onclick={() => void onDiscover()}>
          {discovering ? 'Looking up…' : 'Continue'}
        </button>
      </div>

    {:else if step === 'auth' && discovered}
      <p class="note">
        Settings found for <strong>{discovered.providerName}</strong>
        ({discovered.imap.host} · {discovered.smtp.host}).
      </p>
      {#if discovered.requiresOAuth}
        <p class="note"><strong>Sign-in</strong></p>
        <p class="note">
          {discovered.providerName} requires OAuth2. Signing in opens your browser — Posthaste never sees your
          password, and tokens are stored in your system keyring.
        </p>
        {#if error}<p class="form-error">{error}</p>{/if}
        <div class="pane-foot">
          <span class="grow"></span>
          {#if oauthWaiting}
            <button class="btn" onclick={onCancelOAuth}>Cancel sign-in</button>
          {:else}
            <button class="btn" onclick={() => (step = 'manual')}>Adjust servers manually</button>
          {/if}
          <button class="btn primary" disabled={oauthWaiting} onclick={() => void onOAuth()}>
            {oauthWaiting ? 'Waiting for browser…' : 'Open browser to sign in'}
          </button>
        </div>
      {:else}
        <div class="field">
          <label for="setup-password">Password</label>
          <!-- svelte-ignore a11y_autofocus -->
          <input id="setup-password" data-autofocus class="text-input" type="password" bind:value={password} />
          {#if discovered.appPasswordUrl}
            <p class="note">
              If 2-factor auth is enabled you may need an app password —
              <a class="ext-link" href={discovered.appPasswordUrl} target="_blank" rel="noopener noreferrer">provider instructions</a>.
            </p>
          {/if}
        </div>
        {#if error}<p class="form-error">{error}</p>{/if}
        <div class="pane-foot">
          <span class="grow"></span>
          <button class="btn" onclick={() => (step = 'manual')}>Adjust servers manually</button>
          <button class="btn primary" disabled={verifying} onclick={() => void onVerify()}>
            {verifying ? 'Verifying…' : 'Verify & add account'}
          </button>
        </div>
      {/if}

    {:else}
      <p class="note">Server settings{discovered ? ` (best guess for ${discovered.providerName})` : ''}. Correct any field before verifying.</p>
      <div class="field">
        <label for="setup-email2">Email address</label>
        <input id="setup-email2" class="text-input" type="email" spellcheck="false" bind:value={email} />
      </div>
      <h3>Incoming mail (IMAP)</h3>
      <div class="grid3">
        <div class="field"><label for="imap-host">Host</label><input id="imap-host" class="text-input" bind:value={imapHost} /></div>
        <div class="field"><label for="imap-port">Port</label><input id="imap-port" class="text-input" type="number" bind:value={imapPort} /></div>
        <div class="field">
          <label for="imap-security">Encryption</label>
          <select id="imap-security" class="text-input" bind:value={imapSecurity}>
            {#each securityOptions as option (option)}<option value={option}>{option.toUpperCase()}</option>{/each}
          </select>
        </div>
      </div>
      <h3>Outgoing mail (SMTP)</h3>
      <div class="grid3">
        <div class="field"><label for="smtp-host">Host</label><input id="smtp-host" class="text-input" bind:value={smtpHost} /></div>
        <div class="field"><label for="smtp-port">Port</label><input id="smtp-port" class="text-input" type="number" bind:value={smtpPort} /></div>
        <div class="field">
          <label for="smtp-security">Encryption</label>
          <select id="smtp-security" class="text-input" bind:value={smtpSecurity}>
            {#each securityOptions as option (option)}<option value={option}>{option.toUpperCase()}</option>{/each}
          </select>
        </div>
      </div>
      <div class="field">
        <label for="setup-username">Username</label>
        <input id="setup-username" class="text-input" spellcheck="false" bind:value={username} />
      </div>
      {#if discovered && !discovered.requiresOAuth}
        <div class="field">
          <label for="setup-password2">Password</label>
          <input id="setup-password2" class="text-input" type="password" bind:value={password} />
        </div>
      {/if}
      {#if error}<p class="form-error">{error}</p>{/if}
      <div class="pane-foot">
        <span class="grow"></span>
        <button class="btn" onclick={() => (step = discovered ? 'auth' : 'email')}>Back</button>
        <button class="btn primary" disabled={verifying} onclick={() => void onVerify()}>
          {verifying ? 'Verifying…' : 'Verify & add account'}
        </button>
      </div>
    {/if}
  </div>
</div>
