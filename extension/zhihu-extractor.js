/* DOM-only extraction: no cookies, hidden application state or page execution. */
(function (root) {
  function extract(doc, href, mode, limit = 50) {
    const text = el => (el?.innerText || el?.textContent || '').trim();
    const meta = (el, name) => el.querySelector(`meta[itemprop="${name}"]`)?.content || null;
    const absolute = url => { try { return new URL(url, href).href; } catch { return null; } };
    const base = {schema_version: 1, url: href, captured_at: new Date().toISOString(), source: 'visible_dom'};
    if (mode === 'author') {
      const items = [...doc.querySelectorAll('.ArticleItem')].map(card => {
        const a = card.querySelector('h2 a');
        const url = absolute(a?.getAttribute('href'));
        if (!url || !/^https:\/\/zhuanlan\.zhihu\.com\/p\/\d+$/.test(url)) return null;
        return {id: url.split('/').pop(), url, title: text(a), author: meta(card, 'name'), author_url: card.querySelector('.AuthorInfo a')?.href || null,
          published_at: meta(card, 'datePublished'), updated_at: meta(card, 'dateModified'), excerpt: text(card.querySelector('[itemprop="articleBody"]')),
          metrics_text: [...card.querySelectorAll('button')].map(b => ({label: b.getAttribute('aria-label') || text(b), value: text(b)})), cover_url: meta(card, 'image')};
      }).filter(Boolean);
      const unique = [...new Map(items.map(i => [i.id, i])).values()];
      const totalLabel = [...doc.querySelectorAll('[role="tab"]')].map(text).find(s => /^文章\s*[\d,]+$/.test(s));
      const expectedTotal = totalLabel ? Number(totalLabel.replace(/[^\d]/g, '')) : null;
      return {...base, kind: 'author_posts', author: text(doc.querySelector('h1')), items: unique.slice(0, limit), loaded_count: unique.length,
        expected_total: expectedTotal,
        scope: 'loaded_window', complete: false, stop_reason: unique.length >= limit ? 'limit_reached' : 'loaded_window_end',
        status: unique.length ? 'ok' : /请登录后查看/.test(text(doc.body)) ? 'login_required' : 'not_ready'};
    }
    const body = doc.querySelector('.Post-RichText');
    const title = text(doc.querySelector('h1.Post-Title'));
    const bodyText = text(body);
    const blocked = !!doc.querySelector('.RichContent.is-collapsed,.RichContent-inner--collapsed') || /购买后|付费后|开通会员.*阅读全文/.test(text(doc.body));
    const blocks = body ? [...body.children].map((el, i) => ({index: i + 1, tag: el.tagName.toLowerCase(), text: text(el)})) : [];
    const author = doc.querySelector('.Post-Header .AuthorInfo') || doc.querySelector('.AuthorInfo');
    return {...base, kind: 'article', id: href.match(/\/p\/(\d+)/)?.[1] || null, title, author: text(author?.querySelector('.AuthorInfo-name')), author_url: author?.querySelector('a')?.href || null,
      body: bodyText, body_html: body?.innerHTML || '', blocks, time_text: text(doc.querySelector('.ContentItem-time')),
      images: body ? [...body.querySelectorAll('img')].map(img => ({url: absolute(img.getAttribute('data-original') || img.getAttribute('data-actualsrc') || img.getAttribute('src')), alt: img.alt || ''})).filter(i => i.url?.startsWith('https://')) : [],
      links: body ? [...body.querySelectorAll('a[href]')].map(a => ({url: a.href, text: text(a)})) : [],
      metrics_text: [...doc.querySelectorAll('.ContentItem-actions button')].map(b => ({label: b.getAttribute('aria-label') || text(b), value: text(b)})),
      status: !body || !title || !bodyText ? 'not_ready' : blocked ? 'partial' : 'ok',
      completeness: {body_container_found: !!body, title_found: !!title, text_length: bodyText.length, truncated_by_collector: false, access_block_detected: blocked, last_block: blocks.at(-1)?.text || ''}};
  }
  root.OmniZhihuExtract = extract;
  if (typeof module !== 'undefined') module.exports = extract;
})(typeof globalThis !== 'undefined' ? globalThis : this);
