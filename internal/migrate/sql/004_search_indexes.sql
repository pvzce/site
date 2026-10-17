CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX projects_title_substring ON pvzce_site.projects USING gin(title gin_trgm_ops);
CREATE INDEX projects_description_substring ON pvzce_site.projects USING gin(description gin_trgm_ops);
CREATE INDEX projects_download_sort ON pvzce_site.projects(downloads DESC,id DESC) WHERE state='published';
CREATE INDEX projects_rating_sort ON pvzce_site.projects((CASE WHEN rating_count>0 THEN rating_sum::numeric/rating_count ELSE 0 END) DESC,id DESC) WHERE state='published';
CREATE INDEX projects_kind_sort ON pvzce_site.projects(kind,updated_at DESC,id DESC) WHERE state='published';
