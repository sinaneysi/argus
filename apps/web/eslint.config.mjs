import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { FlatCompat } from "@eslint/eslintrc";

const compat = new FlatCompat({ baseDirectory: dirname(fileURLToPath(import.meta.url)) });

const config = [
  { ignores: [".next/**", "node_modules/**", "public/**", "next-env.d.ts"] },
  ...compat.extends("next/core-web-vitals", "next/typescript"),
  {
    rules: {
      "react/forbid-dom-props": ["error", { forbid: ["style"] }], // strict CSP: no inline styles
      "react/no-danger": "error",
      "@next/next/no-html-link-for-pages": "off", // plain <a> is the choice: pages work with JavaScript off (DESIGN.md 3.24)
    },
  },
];

export default config;
