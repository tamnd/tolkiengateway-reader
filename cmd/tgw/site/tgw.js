/*
 * The reader's own script. It stands in for the parts of Citizen and
 * MediaWiki that run in the browser: dropdowns, the search box, the
 * preferences menu, collapsible sections and tables, the table of contents,
 * the sticky header and CategoryTree. The markup it writes is the markup
 * those modules write, so the vendored stylesheet styles it unchanged.
 */
(function () {
	'use strict';

	var doc = document, body = doc.body, root = doc.documentElement;
	var page = window.TGW_PAGE || {};

	function $(sel, ctx) { return (ctx || doc).querySelector(sel); }
	function $$(sel, ctx) { return Array.prototype.slice.call((ctx || doc).querySelectorAll(sel)); }
	function esc(s) {
		return String(s).replace(/[&<>"]/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]; });
	}
	function siteURL(path) {
		return TGW.base + '/' + tgwWikiEscape(path);
	}

	/* Dropdowns: one open at a time, closed by a click outside or Escape. */
	var dropdowns = $$('.citizen-dropdown-details');
	function closeDropdowns(except) {
		dropdowns.forEach(function (d) { if (d !== except && d.open) d.open = false; });
	}
	dropdowns.forEach(function (d) {
		d.addEventListener('toggle', function () {
			if (d.open) {
				closeDropdowns(d);
				if (d.id === 'citizen-search-details') openSearch();
				if (d.id === 'citizen-preferences-details') buildPreferences();
			}
		});
	});
	doc.addEventListener('click', function (e) {
		var open = dropdowns.filter(function (d) { return d.open; });
		open.forEach(function (d) {
			var container = d.parentNode;
			if (!container.contains(e.target)) d.open = false;
		});
	});
	doc.addEventListener('keydown', function (e) {
		var tag = (e.target.tagName || '').toLowerCase();
		var typing = tag === 'input' || tag === 'textarea' || tag === 'select' || e.target.isContentEditable;
		if (e.key === 'Escape') {
			closeDropdowns();
			if (typing) e.target.blur();
		} else if (e.key === '/' && !typing && !e.ctrlKey && !e.metaKey && !e.altKey) {
			var search = $('#citizen-search-details');
			if (search) {
				e.preventDefault();
				search.open = true;
			}
		}
	});

	/* Search: titles from search.json, shown the way Citizen's typeahead shows them. */
	var searchIndex = null, searchLoading = null, searchBuilt = false;
	// Articles are listed before pages in other namespaces, as the wiki's
	// own search box does by searching articles first.
	var namespaced = /^(Category|File|Template|Tolkien Gateway|Portal|Index|Help|User|Forums?|MediaWiki|Module|Property|Special|[^:]* talk):/;
	function loadIndex() {
		if (!searchLoading) {
			searchLoading = fetch(TGW.assets + '/search.json').then(function (r) { return r.json(); }).then(function (data) {
				searchIndex = data.map(function (e) { return { title: e[0], path: e[1], desc: e[2] || '', target: e[3] || '', image: e[4] || '', key: e[0].toLowerCase(), ns: namespaced.test(e[0]) ? 1 : 0 }; });
				return searchIndex;
			});
		}
		return searchLoading;
	}
	function openSearch() {
		var input = $('#searchInput');
		if (!input) return;
		buildSearch();
		setTimeout(function () { input.focus(); input.select(); }, 0);
		loadIndex();
	}
	function buildSearch() {
		if (searchBuilt) return;
		searchBuilt = true;
		var card = $('#citizen-search__card'), form = $('#searchform'), input = $('#searchInput');
		form.setAttribute('aria-owns', 'searchform-suggestions');
		var group = doc.createElement('div');
		group.className = 'citizen-typeahead-input-group';
		group.innerHTML = '<div class="citizen-typeahead-input-overlay"><span class="citizen-typeahead-input-overlay-query"></span></div>';
		input.parentNode.insertBefore(group, input);
		group.appendChild(input);
		input.className = 'citizen-typeahead-input';
		input.setAttribute('aria-autocomplete', 'list');
		input.setAttribute('aria-controls', 'searchform-suggestions');
		input.setAttribute('aria-activedescendant', '');
		var clear = doc.createElement('span');
		clear.className = 'citizen-search__clear citizen-search__formButton';
		clear.innerHTML = '<span class="citizen-ui-icon mw-ui-icon-wikimedia-trash"></span>';
		clear.hidden = true;
		form.insertBefore(clear, $('.citizen-search__random', form));
		var suggestions = doc.createElement('div');
		suggestions.id = 'searchform-suggestions';
		suggestions.className = 'citizen-typeahead';
		suggestions.innerHTML =
			'<div class="citizen-typeahead-group citizen-typeahead-group--chips" id="citizen-typeahead-group-action" data-mw-typeahead-type="action" hidden><ul class="citizen-typeahead-list" id="citizen-typeahead-list-action" role="listbox"></ul></div>' +
			'<div class="citizen-typeahead-group" id="citizen-typeahead-group-page" data-mw-typeahead-type="page" data-mw-typeahead-keyboard-navigation="" hidden><ul class="citizen-typeahead-list" id="citizen-typeahead-list-page" role="listbox"></ul></div>' +
			'<div class="citizen-typeahead-placeholder" id="citizen-typeahead-placeholder" hidden></div>';
		card.insertBefore(suggestions, $('.citizen-search__footer', card));

		var overlay = $('.citizen-typeahead-input-overlay-query', group);
		var active = -1;
		function items() { return $$('#citizen-typeahead-list-page .citizen-typeahead-list-item-link'); }
		function setActive(i) {
			var links = items();
			links.forEach(function (a, j) { a.toggleAttribute('data-mw-typeahead-selected', j === i); });
			active = i;
			input.setAttribute('aria-activedescendant', i >= 0 && links[i] ? links[i].id : '');
			if (i >= 0 && links[i]) links[i].scrollIntoView({ block: 'nearest' });
		}
		function render() {
			var q = input.value.trim();
			overlay.textContent = input.value;
			clear.hidden = !input.value;
			card.classList.toggle('citizen-search__card--expanded', q !== '');
			var actions = $('#citizen-typeahead-group-action'), pages = $('#citizen-typeahead-group-page'), placeholder = $('#citizen-typeahead-placeholder');
			if (!q) {
				actions.hidden = pages.hidden = placeholder.hidden = true;
				return;
			}
			var enc = encodeURIComponent(q).replace(/%20/g, '+');
			$('ul', actions).innerHTML =
				chip('action-fulltext', TGW.origin + '/w/index.php?title=Special:Search&fulltext=1&search=' + enc, 'articleSearch', tgwT('Advanced search')) +
				chip('action-mediasearch', TGW.origin + '/w/index.php?title=Special:MediaSearch&type=image&search=' + enc, 'imageGallery', tgwT('Search media'));
			actions.hidden = false;
			if (!searchIndex) {
				loadIndex().then(render);
				return;
			}
			var results = search(q, 10);
			$('ul', pages).innerHTML = results.map(function (r, i) { return pageItem(r, i, q); }).join('');
			pages.hidden = results.length === 0;
			placeholder.hidden = results.length !== 0;
			placeholder.innerHTML = results.length ? '' :
				'<div class="citizen-typeahead-placeholder-content"><div class="citizen-typeahead-placeholder-icon citizen-ui-icon mw-ui-icon-wikimedia-articleNotFound"></div>' +
				'<div class="citizen-typeahead-placeholder-title">' + tgwT('No results found') + '</div><div class="citizen-typeahead-placeholder-description">' + tgwT('Try the advanced search on Tolkien Gateway.') + '</div></div>';
			setActive(-1);
		}
		input.addEventListener('input', render);
		input.addEventListener('focus', render);
		clear.addEventListener('click', function () { input.value = ''; render(); input.focus(); });
		input.addEventListener('keydown', function (e) {
			var links = items();
			if (e.key === 'ArrowDown') {
				e.preventDefault();
				setActive(links.length ? (active + 1) % links.length : -1);
			} else if (e.key === 'ArrowUp') {
				e.preventDefault();
				setActive(links.length ? (active <= 0 ? links.length - 1 : active - 1) : -1);
			}
		});
		form.addEventListener('submit', function (e) {
			var q = input.value.trim();
			var links = items();
			if (active >= 0 && links[active]) {
				e.preventDefault();
				location.href = links[active].href;
				return;
			}
			if (searchIndex) {
				var exact = search(q, 1)[0];
				if (exact && exact.key === q.toLowerCase()) {
					e.preventDefault();
					location.href = siteURL(exact.path);
				}
			}
		});
	}
	function chip(id, href, icon, label) {
		return '<li class="citizen-typeahead-list-item"><a href="' + esc(href) + '" class="citizen-typeahead-list-item-link" id="citizen-typeahead-item-' + id + '">' +
			'<div class="citizen-typeahead-list-item-icon citizen-ui-icon mw-ui-icon-wikimedia-' + icon + '"></div><div class="citizen-typeahead-list-item-text">' + esc(label) + '</div></a></li>';
	}
	function pageItem(r, i, q) {
		var title = r.title, at = r.key.indexOf(q.toLowerCase());
		var label = at >= 0 ?
			esc(title.slice(0, at)) + '<span class="citizen-typeahead__highlight">' + esc(title.slice(at, at + q.length)) + '</span>' + esc(title.slice(at + q.length)) :
			esc(title);
		var image = r.image ?
			'<div class="citizen-typeahead-list-item-image" style="background-image: url(' + esc(thumbURL(r.image, 60)) + ');"></div>' :
			'<div class="citizen-typeahead-list-item-image citizen-typeahead-list-item-image--placeholder citizen-ui-icon mw-ui-icon-wikimedia-image"></div>';
		var desc = r.target && r.target !== r.title ? '→ ' + r.target + (r.desc ? ' · ' + r.desc : '') : r.desc;
		return '<li class="citizen-typeahead-list-item"><a href="' + esc(siteURL(r.path)) + '" class="citizen-typeahead-list-item-link" id="citizen-typeahead-item-page-' + i + '">' + image +
			'<div class="citizen-typeahead-list-item-text"><div class="citizen-typeahead-list-item-title">' + label + '</div>' +
			(desc ? '<div class="citizen-typeahead-list-item-description">' + esc(desc) + '</div>' : '') + '</div></a></li>';
	}
	function thumbURL(rel, width) {
		var name = rel.slice(rel.lastIndexOf('/') + 1);
		var thumb = 'thumb/' + rel + '/' + width + 'px-' + name + (/\.svg$/i.test(name) ? '.png' : '');
		return TGW.origin + '/w/images/' + thumb.split('/').map(tgwWikiEscape).join('/');
	}
	// search ranks titles that start with the query first, then titles
	// with a word starting with it, then any other match; redirects to a
	// page already listed are dropped.
	function search(q, limit) {
		var needle = q.toLowerCase(), buckets = [[], [], []];
		for (var i = 0; i < searchIndex.length; i++) {
			var e = searchIndex[i], at = e.key.indexOf(needle);
			if (at < 0) continue;
			var rank = at === 0 ? 0 : /[\s(\-:'"]/.test(e.key.charAt(at - 1)) ? 1 : 2;
			buckets[rank].push(e);
		}
		var out = [], seen = {};
		for (var b = 0; b < 3 && out.length < limit; b++) {
			buckets[b].sort(function (x, y) {
				return x.ns - y.ns || (x.target ? 1 : 0) - (y.target ? 1 : 0) || x.title.length - y.title.length || (x.title < y.title ? -1 : 1);
			});
			for (var j = 0; j < buckets[b].length && out.length < limit; j++) {
				var hit = buckets[b][j];
				if (seen[hit.path] && hit.key !== needle) continue;
				seen[hit.path] = true;
				out.push(hit);
			}
		}
		return out;
	}

	/* Preferences: the Citizen client preferences, kept in localStorage. */
	var preferences = [
		{ key: 'citizen-feature-custom-font-size', label: 'Text', options: [['small', 'Small'], ['standard', 'Standard'], ['large', 'Large']] },
		{ key: 'skin-theme', label: 'Color', options: [['os', 'Auto'], ['day', 'Light'], ['night', 'Dark']] },
		{ key: 'citizen-feature-custom-width', label: 'Width', options: [['standard', 'Standard'], ['wide', 'Wide'], ['full', 'Full']] },
		{ key: 'citizen-feature-pure-black', label: 'Pure black mode', options: [['0', 'Off'], ['1', 'On']] },
		{ key: 'citizen-feature-autohide-navigation', label: 'Automatically hide navigation', options: [['0', 'Off'], ['1', 'On']] }
	];
	function currentPref(key) {
		var m = root.className.match(new RegExp('(?:^|\\s)' + key + '-clientpref-(\\S+)'));
		return m ? m[1] : '';
	}
	function setPref(key, value) {
		root.className = root.className.replace(new RegExp('(^|\\s)' + key + '-clientpref-\\S+'), '$1' + key + '-clientpref-' + value);
		var saved = {};
		try { saved = JSON.parse(localStorage.getItem('tgw-clientprefs')) || {}; } catch (e) {}
		saved[key] = value;
		try { localStorage.setItem('tgw-clientprefs', JSON.stringify(saved)); } catch (e) {}
		updateThemeColor();
	}
	function buildPreferences() {
		var content = $('#citizen-preferences-content');
		if (!content || content.childNodes.length) return;
		content.innerHTML = preferences.map(function (p) {
			var radios = p.options.map(function (o) {
				var id = 'skin-client-pref-' + p.key + '-value-' + o[0];
				return '<div class="citizen-client-prefs-radio"><input name="skin-client-pref-' + p.key + '-group" id="' + id + '" type="radio" value="' + o[0] + '" data-event-name="' + id +
					'" class="citizen-client-prefs-radio__input"' + (currentPref(p.key) === o[0] ? ' checked' : '') + '><span class="citizen-client-prefs-radio__icon"></span><label for="' + id +
					'" class="citizen-client-prefs-radio__label">' + tgwT(o[1]) + '</label></div>';
			}).join('');
			return '<div class="mw-portlet mw-portlet-skin-client-prefs-' + p.key + ' mw-portlet-js citizen-menu" id="skin-client-prefs-' + p.key + '"><div class="citizen-menu__heading">' + tgwT(p.label) +
				'</div><div class="citizen-menu__content"><ul class="citizen-menu__content-list"><li class="mw-list-item mw-list-item-js"><div class=""><form>' + radios + '</form></div></li></ul></div></div>';
		}).join('');
		content.addEventListener('change', function (e) {
			var input = e.target, m = input.name.match(/^skin-client-pref-(.+)-group$/);
			if (m) setPref(m[1], input.value);
		});
	}
	function updateThemeColor() {
		var meta = $('meta[name="theme-color"]');
		if (meta) meta.content = getComputedStyle(body).getPropertyValue('--color-surface-0').trim() || meta.content;
	}

	/* Collapsible sections, as Citizen's sections module does it. */
	$$('.citizen-section-heading').forEach(function (heading) {
		var section = heading.nextElementSibling;
		if (!section || !section.classList.contains('citizen-section')) return;
		heading.addEventListener('click', function (e) {
			if (e.target.closest('a, button, .mw-editsection')) return;
			if (window.getSelection && String(window.getSelection())) return;
			if (section.hidden) section.hidden = false;
			else section.setAttribute('hidden', 'until-found');
		});
	});
	function revealTarget() {
		var id = decodeURIComponent(location.hash.slice(1));
		var target = id && doc.getElementById(id);
		if (!target) return;
		for (var el = target; el; el = el.parentElement) {
			if (el.classList && el.classList.contains('citizen-section') && el.hidden) el.hidden = false;
		}
	}
	window.addEventListener('hashchange', revealTarget);
	revealTarget();

	/* Table of contents: toggles, and the heading in view highlighted. */
	$$('.citizen-toc-toggle').forEach(function (button) {
		button.addEventListener('click', function (e) {
			e.preventDefault();
			var item = button.closest('.citizen-toc-list-item');
			var expanded = item.classList.toggle('citizen-toc-list-item--expanded');
			button.setAttribute('aria-expanded', expanded ? 'true' : 'false');
		});
	});
	$$('#mw-panel-toc a').forEach(function (a) {
		a.addEventListener('click', function () {
			var details = $('#citizen-toc .citizen-dropdown-details');
			if (details) details.open = false;
		});
	});
	var tocLinks = $$('#mw-panel-toc-list .citizen-toc-link');
	if (tocLinks.length && 'IntersectionObserver' in window) {
		var headings = tocLinks.map(function (a) { return doc.getElementById(decodeURIComponent(a.getAttribute('href').slice(1))); });
		var activeItem = null;
		var activate = function () {
			var line = (parseFloat(getComputedStyle(root).scrollPaddingTop) || 0) + 20, current = -1;
			for (var i = 0; i < headings.length; i++) {
				var h = headings[i];
				if (!h || !h.getClientRects().length) continue;
				if (h.getBoundingClientRect().top <= line) current = i; else break;
			}
			var item = current >= 0 ? tocLinks[current].parentNode : null;
			if (item === activeItem) return;
			$$('.citizen-toc-list-item--active').forEach(function (li) { li.classList.remove('citizen-toc-list-item--active'); });
			$$('.citizen-toc-level-1.citizen-toc-list-item--expanded[data-auto]').forEach(function (li) {
				li.classList.remove('citizen-toc-list-item--expanded');
				li.removeAttribute('data-auto');
			});
			activeItem = item;
			for (var li = item; li && li.classList.contains('citizen-toc-list-item'); li = li.parentNode.closest('.citizen-toc-list-item')) {
				li.classList.add('citizen-toc-list-item--active');
				if (li.classList.contains('citizen-toc-level-1') && !li.classList.contains('citizen-toc-list-item--expanded')) {
					li.classList.add('citizen-toc-list-item--expanded');
					li.setAttribute('data-auto', '');
				}
			}
		};
		var pending = false;
		window.addEventListener('scroll', function () {
			if (pending) return;
			pending = true;
			requestAnimationFrame(function () { pending = false; activate(); });
		}, { passive: true });
		activate();
	}

	/* Sticky page header and the scroll direction, which autohide keys on. */
	var sentinel = $('#citizen-page-header-sticky-sentinel');
	if (sentinel && 'IntersectionObserver' in window) {
		new IntersectionObserver(function (entries) {
			var e = entries[0];
			body.classList.toggle('citizen-sticky-header-visible', !e.isIntersecting && e.boundingClientRect.top < 0);
		}).observe(sentinel);
	}
	var lastY = window.scrollY, ticking = false;
	window.addEventListener('scroll', function () {
		if (ticking) return;
		ticking = true;
		requestAnimationFrame(function () {
			var y = window.scrollY;
			if (Math.abs(y - lastY) > 5) {
				var down = y > lastY && y > 100;
				body.classList.toggle('citizen-scroll--down', down);
				body.classList.toggle('citizen-scroll--up', !down);
				lastY = y;
			}
			ticking = false;
		});
	}, { passive: true });

	/* jquery.makeCollapsible. */
	function makeCollapsible(el) {
		if (el.classList.contains('mw-made-collapsible')) return;
		el.classList.add('mw-made-collapsible');
		var id = el.id || '', custom = id.indexOf('mw-customcollapsible-') === 0;
		var parts;
		var tag = el.tagName;
		function collect() {
			if (tag === 'TABLE') {
				var rows = $$(':scope > tbody > tr, :scope > thead > tr, :scope > tfoot > tr, :scope > tr', el);
				return el.querySelector(':scope > caption') ? rows : rows.slice(1);
			}
			if (tag === 'UL' || tag === 'OL') return $$(':scope > li:not(.mw-collapsible-toggle-li)', el);
			var content = el.querySelector(':scope > .mw-collapsible-content');
			if (!content) {
				content = doc.createElement('div');
				content.className = 'mw-collapsible-content';
				while (el.firstChild) content.appendChild(el.firstChild);
				el.appendChild(content);
			}
			return [content];
		}
		parts = collect();
		var toggles = [];
		function set(collapsed) {
			el.classList.toggle('mw-collapsed', collapsed);
			parts.forEach(function (p) { p.style.display = collapsed ? 'none' : ''; });
			toggles.forEach(function (t) {
				t.classList.toggle('mw-collapsible-toggle-collapsed', collapsed);
				t.classList.toggle('mw-collapsible-toggle-expanded', !collapsed);
				t.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
				var text = t.querySelector('.mw-collapsible-text');
				if (text) text.textContent = collapsed ? (el.getAttribute('data-expandtext') || tgwT('Expand')) : (el.getAttribute('data-collapsetext') || tgwT('Collapse'));
			});
		}
		function bind(t) {
			toggles.push(t);
			t.addEventListener('click', function (e) {
				if (e.target.closest('a') && !e.target.closest('.mw-collapsible-text')) return;
				e.preventDefault();
				set(!el.classList.contains('mw-collapsed'));
			});
			t.addEventListener('keydown', function (e) {
				if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); set(!el.classList.contains('mw-collapsed')); }
			});
		}
		if (custom) {
			$$('.mw-customtoggle-' + id.slice('mw-customcollapsible-'.length)).forEach(bind);
		} else {
			var toggle = el.querySelector('.mw-collapsible-toggle');
			if (!toggle) {
				toggle = doc.createElement('button');
				toggle.type = 'button';
				toggle.className = 'mw-collapsible-toggle mw-collapsible-toggle-default';
				toggle.innerHTML = '<span class="mw-collapsible-text"></span>';
				if (tag === 'TABLE') {
					var caption = el.querySelector(':scope > caption');
					var first = $$(':scope > tbody > tr, :scope > thead > tr, :scope > tr', el)[0];
					var cell = caption || (first && first.lastElementChild);
					if (cell) cell.insertBefore(toggle, cell.firstChild);
				} else if (tag === 'UL' || tag === 'OL') {
					var li = doc.createElement('li');
					li.className = 'mw-collapsible-toggle-li';
					li.appendChild(toggle);
					el.insertBefore(li, el.firstChild);
				} else {
					el.insertBefore(toggle, el.firstChild);
				}
			}
			bind(toggle);
		}
		set(el.classList.contains('mw-collapsed'));
	}
	$$('.mw-collapsible').forEach(makeCollapsible);

	/* CategoryTree: expanding a subcategory lists its own subcategories. */
	doc.addEventListener('click', function (e) {
		var toggle = e.target.closest && e.target.closest('.CategoryTreeToggle');
		if (!toggle) return;
		e.preventDefault();
		var section = toggle.closest('.CategoryTreeSection'), children = $('.CategoryTreeChildren', section);
		var expanded = toggle.getAttribute('data-ct-state') === 'expanded';
		toggle.setAttribute('data-ct-state', expanded ? 'collapsed' : 'expanded');
		toggle.title = tgwT(expanded ? 'expand' : 'collapse');
		children.style.display = expanded ? 'none' : '';
		if (expanded || children.getAttribute('data-loaded')) return;
		children.setAttribute('data-loaded', '1');
		children.innerHTML = '<i class="CategoryTreeNotice">' + tgwT('Loading…') + '</i>';
		var link = $('.CategoryTreeItem > a', section);
		fetch(link.href).then(function (r) { return r.text(); }).then(function (html) {
			var other = new DOMParser().parseFromString(html, 'text/html');
			var items = $$('#mw-subcategories .CategoryTreeSection', other);
			children.innerHTML = items.length ? '' : '<i class="CategoryTreeNotice">no subcategories</i>';
			items.forEach(function (item) { children.appendChild(doc.importNode(item, true)); });
		}).catch(function () {
			children.removeAttribute('data-loaded');
			children.innerHTML = '';
		});
	});
	$$('.CategoryTreeToggle').forEach(function (t) { t.classList.add('CategoryTreeToggleHandlerAttached'); });

	/* Last modified: relative time in the reader's own clock. */
	var lastmod = $('#citizen-lastmod-relative');
	if (lastmod && window.Intl && Intl.RelativeTimeFormat) {
		var seconds = (Date.now() / 1000) - Number(lastmod.getAttribute('data-timestamp'));
		var units = [['year', 31536000], ['month', 2592000], ['week', 604800], ['day', 86400], ['hour', 3600], ['minute', 60], ['second', 1]];
		var rtf = new Intl.RelativeTimeFormat(TGW.lang, { numeric: 'auto' });
		for (var u = 0; u < units.length; u++) {
			if (seconds >= units[u][1] || u === units.length - 1) {
				var span = lastmod.querySelector('span:last-child');
				if (span) span.textContent = rtf.format(-Math.floor(seconds / units[u][1]), units[u][0]);
				break;
			}
		}
	}

	/* Share: the system share sheet, or the link copied. */
	var share = $('#citizen-share');
	if (share) {
		share.addEventListener('click', function () {
			var data = { title: doc.title, url: location.href };
			if (navigator.share) {
				navigator.share(data).catch(function () {});
			} else if (navigator.clipboard) {
				navigator.clipboard.writeText(location.href).then(function () {
					share.setAttribute('title', tgwT('Link copied'));
					setTimeout(function () { share.setAttribute('title', tgwT('Share this page')); }, 2000);
				});
			}
		});
	}

	requestAnimationFrame(function () { root.classList.add('citizen-animations-ready'); });
	if (page) updateThemeColor();
})();
