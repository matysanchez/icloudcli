@../CLAUDE.md

# web/ — icloudcli.com static site

- **Single-file site**: `index.html` is the entire landing page. All CSS and JS are inline.
- **Deploy**: `npx wrangler pages deploy . --project-name icloudcli`
- **Images**: `apple2e.avif` (53 KB) and `apple2e.webp` (123 KB) are optimised versions of `apple2e.png`; use `<picture>` with `loading="lazy"`.

## Newsletter form (embed.js convention)

State elements are controlled by `embed.js` via `[hidden]` attribute toggling:

| Selector | Purpose |
|---|---|
| `[data-leads-inputs]` | Form inputs + submit button (visible by default) |
| `[data-leads-success]` | Shown on successful signup |
| `[data-leads-already]` | Shown when email already registered |
| `[data-leads-error]` | Shown on error (text set by embed.js) |

**Critical CSS rule** — always include this to prevent hidden state leaking:
```css
.nl-msg[hidden] { display: none; }
```
Without it, any `display: block` rule on `.nl-msg` overrides the `[hidden]` attribute and shows all messages before form interaction.
