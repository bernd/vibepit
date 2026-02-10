# Vibepit Theme

A cyberpunk UI palette inspired by the Vibepit logo and tuned for readability,
consistency, and accessibility. It is intentionally curated, not a literal pixel
extraction.

---

## Brand-Inspired Palette

| Name | Hex | Use |
|------|-----|-----|
| Neon Cyan | `#00d4ff` | Primary brand, glows |
| Glow Cyan | `#00f0f0` | Highlights, hover states |
| Deep Teal | `#0099cc` | Primary dark, borders |
| Violet | `#8b5cf6` | Secondary accent |
| Purple Haze | `#6d28d9` | Secondary dark |
| Flame Orange | `#f97316` | Call-to-action, emphasis |
| Sunset Red | `#ea580c` | Accent dark |
| Void Black | `#0a0a0a` | Background |
| Midnight Blue | `#0d1829` | Elevated surfaces |
| Circuit Blue | `#1a2744` | Cards, panels |
| Ice White | `#e0f7ff` | Primary text |
| Sky Glow | `#7dd3fc` | Secondary text |
| Muted Teal | `#4a90a4` | Disabled, hints |
| Robot Gray | `#6b7280` | Icons, borders |

## System/Semantic Palette

| Name | Hex | Use |
|------|-----|-----|
| Success Green | `#10b981` | Success states |
| Warning Amber | `#f59e0b` | Warning states |
| Error Red | `#ef4444` | Error states |
| Matrix Green (Alt) | `#22d3a0` | Secondary success accent |

---

## CSS Variables

```css
:root {
  /* Primary - Cyan/Teal glow */
  --color-primary: #00d4ff;
  --color-primary-light: #00f0f0;
  --color-primary-dark: #0099cc;
  
  /* Secondary - Purple accent */
  --color-secondary: #8b5cf6;
  --color-secondary-light: #a78bfa;
  --color-secondary-dark: #6d28d9;
  
  /* Accent - Orange/Red from tagline */
  --color-accent: #f97316;
  --color-accent-light: #fb923c;
  --color-accent-dark: #ea580c;
  
  /* Background - Deep blacks and dark blues */
  --color-bg-primary: #0a0a0a;
  --color-bg-secondary: #0d1829;
  --color-bg-elevated: #1a2744;
  
  /* Text */
  --color-text-primary: #e0f7ff;
  --color-text-secondary: #7dd3fc;
  --color-text-muted: #4a90a4;
  
  /* Semantic */
  --color-success: #10b981;
  --color-warning: #f59e0b;
  --color-error: #ef4444;
  
  /* Glow effects */
  --glow-cyan: 0 0 20px rgba(0, 212, 255, 0.5);
  --glow-purple: 0 0 20px rgba(139, 92, 246, 0.5);
  --glow-orange: 0 0 15px rgba(249, 115, 22, 0.6);
  
  /* Gradients */
  --gradient-cyber: linear-gradient(135deg, #00d4ff 0%, #8b5cf6 100%);
  --gradient-pit: radial-gradient(ellipse at center, #0099cc 0%, #0d1829 70%, #0a0a0a 100%);
  --gradient-title: linear-gradient(180deg, #00d4ff 0%, #0099cc 50%, #8b5cf6 100%);
}
```

---

## Example Usage

### Base Styles

```css
body {
  background: var(--color-bg-primary);
  color: var(--color-text-primary);
  font-family: system-ui, -apple-system, sans-serif;
}
```

### Card Component

```css
.card {
  background: var(--color-bg-elevated);
  border: 1px solid var(--color-primary-dark);
  border-radius: 8px;
  box-shadow: var(--glow-cyan);
  padding: 1.5rem;
}
```

### Buttons

```css
.button-primary {
  background: var(--gradient-cyber);
  color: var(--color-bg-primary);
  border: none;
  padding: 0.75rem 1.5rem;
  border-radius: 4px;
  font-weight: 600;
  cursor: pointer;
  transition: box-shadow 0.2s ease;
}

.button-primary:hover {
  box-shadow: var(--glow-cyan);
}

.button-accent {
  background: var(--color-accent);
  color: var(--color-bg-primary);
  border: none;
  padding: 0.75rem 1.5rem;
  border-radius: 4px;
  font-weight: 600;
  cursor: pointer;
}

.button-accent:hover {
  box-shadow: var(--glow-orange);
}
```

### Headings with Glow

```css
.heading-glow {
  background: var(--gradient-title);
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
  filter: drop-shadow(0 0 10px rgba(0, 212, 255, 0.5));
}
```

### Links

```css
a {
  color: var(--color-primary);
  text-decoration: none;
  transition: color 0.2s ease, text-shadow 0.2s ease;
}

a:hover {
  color: var(--color-primary-light);
  text-shadow: var(--glow-cyan);
}
```

---

## Design Notes

The core palette uses five colors working together:

1. **Cyan** (`#00d4ff`) — The dominant glow, represents the arena energy
2. **Purple** (`#8b5cf6`) — Supporting accent, adds depth and mystery
3. **Orange** (`#f97316`) — High contrast punch for CTAs and emphasis
4. **Black** (`#0a0a0a`) — Deep void background
5. **Dark Blue** (`#0d1829` → `#1a2744`) — Gradient for layered depth

Accessibility usage note:
Use `--color-secondary-dark` and similarly dark accent tones for decorative
elements, not small text on dark backgrounds.

The aesthetic is inspired by cyberpunk arenas, circuit boards, and neon lighting. Use glow effects sparingly for emphasis, and maintain high contrast between text and backgrounds for readability.
