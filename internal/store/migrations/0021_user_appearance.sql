-- Per-user appearance (docs/specs/02-ux.md#visual-language): the color
-- palette (default, catppuccin, gruvbox, base16, nord), shown in the user's
-- light/dark mode (users.theme), and the interface size in percent.
ALTER TABLE users ADD COLUMN palette TEXT NOT NULL DEFAULT 'default';
ALTER TABLE users ADD COLUMN ui_scale INTEGER NOT NULL DEFAULT 120;
