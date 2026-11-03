-- 0011: English is the product's default language (D60), Malay one click away. An install still on the old
-- default moves; a language an operator chose on purpose stays (only the seeded value is touched).
UPDATE settings SET value = 'en-US', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE key = 'site.default_language' AND value = 'ms-MY' AND updated_by IS NULL;
