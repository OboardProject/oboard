/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  // Theme is applied as both `class="dark"` and `data-theme="dark"` on <html>.
  // Keep class mode so Tailwind `dark:` variants work reliably with the ripple toggle.
  darkMode: "class",
  theme: {
    extend: {
      borderRadius: {
        lg: "var(--radius-lg, 12px)",
        md: "var(--radius-md, 8px)",
        sm: "var(--radius-sm, 6px)",
      },
      colors: {
        border: "var(--border)",
        input: "var(--border)",
        ring: "var(--primary-soft)",
        background: "var(--bg)",
        // Default readable body/title color (not the muted secondary).
        foreground: "var(--text-strong)",
        primary: {
          DEFAULT: "var(--primary)",
          foreground: "var(--primary-contrast)",
        },
        secondary: {
          DEFAULT: "var(--surface-2)",
          foreground: "var(--text-strong)",
        },
        destructive: {
          DEFAULT: "var(--danger)",
          foreground: "#ffffff",
        },
        muted: {
          DEFAULT: "var(--surface-2)",
          foreground: "var(--text-secondary)",
        },
        accent: {
          DEFAULT: "var(--bg-soft)",
          foreground: "var(--text-strong)",
        },
        popover: {
          DEFAULT: "var(--surface-solid)",
          foreground: "var(--text-strong)",
        },
        card: {
          DEFAULT: "var(--surface-solid)",
          foreground: "var(--text-strong)",
        },
      },
      boxShadow: {
        apple: "0 4px 30px rgba(0, 0, 0, 0.03)",
        "apple-hover": "0 10px 40px rgba(0, 0, 0, 0.06)",
        card: "var(--shadow)",
        "card-soft": "var(--shadow-soft)",
      }
    },
  },
  plugins: [],
}
