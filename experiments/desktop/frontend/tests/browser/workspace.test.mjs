// Browser tests for the workspace playground. They drive the built page in
// headless Chromium with real layout, focus and pointer events.
// Run with `npm run test:browser`, after a one-time
// `npx playwright install chromium-headless-shell`.
import { after, before, beforeEach, afterEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { preview } from 'vite';

const root = fileURLToPath(new URL('../..', import.meta.url));
const output = fileURLToPath(new URL('./output/', import.meta.url));
let server, browser, context, page, url, errors;

before(async () => {
  mkdirSync(output, { recursive: true });
  server = await preview({ root, logLevel: 'silent', preview: { host: '127.0.0.1', port: 4319 } });
  url = `${server.resolvedUrls.local[0]}workspace.html`;
  browser = await chromium.launch();
});
after(async () => {
  await browser?.close();
  await server?.close();
});
// Each test starts from the example tabs with empty storage.
beforeEach(async () => {
  context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  page = await context.newPage();
  errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  await page.goto(url);
  await page.waitForSelector('.pane');
});
afterEach(async () => {
  assert.deepEqual(errors, [], 'the page reported errors');
  await context.close();
});

const shown = () => page.locator('.pane:not([hidden])');
const pane = (name) => page.locator(`.pane[aria-label="${name}"]`);
const tabLabels = () => page.locator('.tab .tablabel').allTextContents();
const shownNames = () => shown().evaluateAll((els) => els.map((e) => e.getAttribute('aria-label')));
async function paneAction(name, item) {
  await pane(name).locator('.more').click();
  await page.getByRole('menuitem', { name: item, exact: true }).click();
}
async function tabAction(index, item) {
  await page.locator('.tabmenu').nth(index).click();
  await page.getByRole('menuitem', { name: item, exact: true }).click();
}
const center = async (locator) => { const b = await locator.boundingBox(); return { ...b, x: b.x + b.width / 2, y: b.y + b.height / 2 }; };
// A drag in small steps, so the docking controller sees every stage of it.
// A point with `rest` holds the pointer there, as a tab needs before it becomes the target.
const rest = (p) => ({ x: p.x, y: p.y, rest: 250 });
async function drag(from, points, { drop = true } = {}) {
  await page.mouse.move(from.x, from.y);
  await page.mouse.down();
  await page.mouse.move(from.x + 12, from.y + 12, { steps: 3 });
  for (const p of points) {
    await page.mouse.move(p.x, p.y, { steps: 8 });
    if (p.rest) await page.waitForTimeout(p.rest);
  }
  if (drop) await page.mouse.up();
}

test('the example layout renders', async () => {
  assert.deepEqual(await tabLabels(), ['Codex + Tests', 'Logs', 'Monitor']);
  assert.deepEqual(await shownNames(), ['Codex', 'Tests']);
  assert.equal(await page.locator('.pane').count(), 4, 'panes of background tabs stay mounted');
  assert.equal(await page.locator('.gin').count(), 0, 'the root group has no header');
  const [codex, tests] = [await pane('Codex').boundingBox(), await pane('Tests').boundingBox()];
  assert.ok(codex.x + codex.width <= tests.x, 'Codex is left of Tests');
  assert.ok(Math.abs(codex.width - tests.width) < 2, 'equal widths');
  await page.screenshot({ path: `${output}example-1440x900.png` });
  await page.setViewportSize({ width: 1024, height: 640 });
  await page.waitForTimeout(100);
  const stage = await page.locator('.stage').boundingBox();
  const last = await pane('Tests').boundingBox();
  assert.ok(last.x + last.width <= stage.x + stage.width + 1, 'panes fit a 1024 window without overflow');
  await page.screenshot({ path: `${output}example-1024x640.png` });
});

test('split right joins the row, split below nests, close promotes', async () => {
  {
    await paneAction('Tests', 'Split right');
    assert.deepEqual(await shownNames(), ['Codex', 'Tests', 'Shell 1']);
    assert.equal(await page.locator('.gin').count(), 0);
    assert.equal(await page.evaluate(() => document.activeElement?.closest('.pane')?.getAttribute('aria-label')), 'Shell 1');
    const [tests, shell] = [await pane('Tests').boundingBox(), await pane('Shell 1').boundingBox()];
    assert.ok(Math.abs(tests.width - shell.width) < 2, 'the new shell takes half of the split pane');
  }
  {
    await paneAction('Shell 1', 'Split below');
    assert.equal(await page.locator('.gin').count(), 1);
    assert.equal(await page.locator('.gin b').textContent(), 'Shell 1 + Shell 2');
    assert.deepEqual(await tabLabels(), ['Codex + 3', 'Logs', 'Monitor']);
    const [a, b] = [await pane('Shell 1').boundingBox(), await pane('Shell 2').boundingBox()];
    assert.ok(a.y + a.height <= b.y, 'Shell 2 is below Shell 1');
    await page.screenshot({ path: `${output}nested-1440x900.png` });
  }
  {
    await paneAction('Shell 2', 'Close pane');
    assert.deepEqual(await shownNames(), ['Codex', 'Tests', 'Shell 1']);
    assert.equal(await page.locator('.gin').count(), 0);
  }
});

test('a split that cannot fit leaves the layout intact and says why', async () => {
  await page.setViewportSize({ width: 340, height: 400 });
  await page.waitForTimeout(100);
  await paneAction('Tests', 'Split right');
  assert.deepEqual(await shownNames(), ['Codex', 'Tests']);
  assert.match(await page.locator('.toast').textContent(), /Too little space/);
  await page.keyboard.press('Shift');
  assert.equal(await page.locator('.toast').textContent(), '', 'the message clears on the next action');
});

test('zoom fills the workspace and restores the same layout', async () => {
  await paneAction('Tests', 'Split below');
  const before = await Promise.all(['Codex', 'Tests', 'Shell 1'].map((n) => pane(n).boundingBox()));
  await pane('Codex').locator('input').fill('unsent command');
  await pane('Codex').locator('.zoom-toggle').click();
  assert.deepEqual(await shownNames(), ['Codex']);
  assert.equal(await pane('Codex').locator('.zoom-toggle').textContent(), 'Restore layout');
  const stage = await page.locator('.stage-inner').boundingBox();
  const zoomed = await pane('Codex').boundingBox();
  assert.ok(Math.abs(zoomed.width - stage.width) < 2 && Math.abs(zoomed.height - stage.height) < 2, 'the pane fills the workspace');
  assert.equal(await page.locator('.tabbar').isVisible(), true, 'the tab bar stays available');
  await pane('Codex').locator('.zoom-toggle').click();
  assert.deepEqual(await Promise.all(['Codex', 'Tests', 'Shell 1'].map((n) => pane(n).boundingBox())), before, 'exact proportions return');
  assert.equal(await pane('Codex').locator('input').inputValue(), 'unsent command');
  assert.equal(await page.evaluate(() => document.activeElement?.closest('.pane')?.getAttribute('aria-label')), 'Codex');
});

test('dividers resize neighbors by pointer and by keyboard', async () => {
  const divider = page.locator('.divider');
  const start = Number(await divider.getAttribute('aria-valuenow'));
  const c = await center(divider);
  await page.mouse.move(c.x, c.y);
  await page.mouse.down();
  await page.mouse.move(c.x + 200, c.y, { steps: 5 });
  await page.mouse.up();
  const dragged = Number(await divider.getAttribute('aria-valuenow'));
  assert.ok(dragged > start + 5, `pointer drag grows the first pane: ${start} -> ${dragged}`);
  await divider.focus();
  await page.keyboard.press('ArrowLeft');
  assert.ok(Number(await divider.getAttribute('aria-valuenow')) < dragged, 'ArrowLeft shrinks it');
  await page.keyboard.press('ArrowUp');
  assert.ok(Number(await divider.getAttribute('aria-valuenow')) < dragged, 'ArrowUp does nothing on a vertical divider');
  await page.mouse.move(c.x - 2000, c.y);
  const codex = await pane('Codex').boundingBox();
  assert.ok(codex.width >= 124, 'a pane never goes under its minimum width');
});

test('worked example: drag the Codex + Tests tab onto Logs', async () => {
  const cases = [
    { edge: 'top', title: 'Codex + 2', point: (s) => ({ x: s.x + s.width / 2, y: s.y + 10 }), check: (c, ts, l) => c.x < ts.x && c.y === ts.y && l.y > c.y && Math.abs(l.width - (ts.x + ts.width - c.x)) < 4 },
    { edge: 'right', title: 'Logs + 2', point: (s) => ({ x: s.x + s.width - 10, y: s.y + s.height / 2 }), check: (c, ts, l) => l.x < c.x && c.x === ts.x && c.y < ts.y },
  ];
  for (const c of cases) {
    {
      await page.getByRole('button', { name: 'Restore example tabs' }).click();
      await pane('Tests').locator('input').fill('still here');
      const from = await center(page.locator('.tab-name').nth(0));
      const logsTab = await center(page.locator('.tab').nth(1));
      const stage = await page.locator('.stage-inner').boundingBox();
      // Straight down from the tab, so the pointer crosses no other tab on its way.
      await drag(from, [rest(logsTab), { x: logsTab.x, y: stage.y + 80 }, c.point(stage)], { drop: false });
      const labels = await page.locator('.projection .leafmini').allTextContents();
      assert.deepEqual([...labels].sort(), ['Codex', 'Logs', 'Tests'], 'the preview draws the panes with their titles');
      assert.deepEqual(await tabLabels(), ['Codex + Tests', 'Logs', 'Monitor'], 'a preview causes no structural edit');
      if (c.edge === 'top') await page.screenshot({ path: `${output}docking-preview.png` });
      await page.mouse.up();
      assert.deepEqual(await tabLabels(), [c.title, 'Monitor']);
      const [codex, tests, logs] = await Promise.all(['Codex', 'Tests', 'Logs'].map((n) => pane(n).boundingBox()));
      assert.ok(c.check(codex, tests, logs), `layout after a ${c.edge} drop: ${JSON.stringify({ codex, tests, logs })}`);
      assert.equal(await page.locator('.gin b').textContent(), 'Codex + Tests', 'the pair keeps a common handle');
      assert.equal(await pane('Tests').locator('input').inputValue(), 'still here', 'views keep their state');
    }
  }

  // The group handle carries both panes back to the tab bar.
  {
    const from = await center(page.locator('.gin b'));
    const bar = await page.locator('.tabbar').boundingBox();
    await drag(from, [{ x: bar.x + bar.width - 40, y: bar.y + bar.height / 2 }]);
    assert.deepEqual(await tabLabels(), ['Logs', 'Monitor', 'Codex + Tests']);
  }
});

test('crossing a tab on the way to the workspace does not retarget, resting on it does', async () => {
  const from = await center(pane('Tests').locator('.phead strong'));
  const monitorTab = await center(page.locator('.tab').nth(2));
  const stage = await page.locator('.stage-inner').boundingBox();
  // The left edge, because Tests already sits at the right edge of its own tab.
  const edge = { x: stage.x + 10, y: stage.y + stage.height / 2 };
  const previewed = async () => (await page.locator('.projection .leafmini').allTextContents()).sort();

  await drag(from, [monitorTab, edge], { drop: false });
  assert.deepEqual(await previewed(), ['Codex', 'Tests'], 'a quick pass over the Monitor tab keeps the current tab as target');
  assert.equal(await page.locator('.tab.reveal').count(), 1);
  assert.equal(await page.locator('.tab.reveal .tablabel').textContent(), 'Codex + Tests');

  await page.mouse.move(monitorTab.x, monitorTab.y, { steps: 8 });
  await page.waitForTimeout(250);
  assert.equal(await page.locator('.tab.reveal .tablabel').textContent(), 'Monitor', 'resting on the tab makes it the target without moving the pointer');
  assert.deepEqual(await previewed(), ['Monitor', 'Tests']);
  await page.mouse.up();
  assert.deepEqual(await tabLabels(), ['Codex', 'Logs', 'Monitor + Tests']);
});

test('the docking preview only changes when the pointer pushes toward a side', async () => {
  await page.getByRole('button', { name: 'New Session tab' }).click();
  await page.locator('.tab-name').nth(0).click();
  const from = await center(page.locator('.tab-name').nth(3));
  const body = await pane('Codex').locator('.body').boundingBox();
  const mid = { x: body.x + body.width / 2, y: body.y + body.height / 2 };
  // The arrangement of the preview, as the incoming pane's position relative to Codex.
  const arrangement = async () => page.evaluate(() => {
    const box = (name) => [...document.querySelectorAll('.projection .leafmini')].find((e) => e.textContent === name)?.getBoundingClientRect();
    const a = box('Shell 1'), b = box('Codex');
    if (!a || !b) return 'none';
    return a.right <= b.left + 1 ? 'left' : a.left >= b.right - 1 ? 'right' : a.bottom <= b.top + 1 ? 'above' : 'below';
  });

  await drag(from, [{ x: body.x + 40, y: mid.y }], { drop: false });
  assert.equal(await arrangement(), 'left', 'pushing toward the left side docks on the left');

  await page.mouse.move(mid.x, mid.y, { steps: 10 });
  const seen = new Set([await arrangement()]);
  for (const [dx, dy] of [[14, -12], [-10, 15], [18, 9], [-16, -14], [6, 20], [-20, 4], [0, 0]]) {
    await page.mouse.move(mid.x + dx, mid.y + dy, { steps: 2 });
    seen.add(await arrangement());
  }
  assert.deepEqual([...seen], ['left'], 'small movements in the middle of the pane keep the preview');

  await page.mouse.move(mid.x, body.y + body.height - 30, { steps: 10 });
  assert.equal(await arrangement(), 'below', 'pushing toward the bottom changes it');
  await page.screenshot({ path: `${output}docking-pane-edge.png` });
  await page.mouse.up();
  const [codex, shell] = [await pane('Codex').boundingBox(), await pane('Shell 1').boundingBox()];
  assert.ok(shell.y >= codex.y + codex.height && Math.abs(shell.x - codex.x) < 2, 'the drop matches the last preview');
});

test('dragging the middle of three shells to the left reorders it without resizing', async () => {
  await page.getByRole('button', { name: 'New Session tab' }).click();
  await paneAction('Shell 1', 'Split right');
  await paneAction('Shell 2', 'Split right');
  // Make the middle pane wider, so a resize would show.
  const divider = await center(page.locator('.divider').nth(1));
  await page.mouse.move(divider.x, divider.y); await page.mouse.down(); await page.mouse.move(divider.x + 120, divider.y, { steps: 5 }); await page.mouse.up();
  const widths = async () => Object.fromEntries(await Promise.all(['Shell 1', 'Shell 2', 'Shell 3'].map(async (n) => [n, Math.round((await pane(n).boundingBox()).width)])));
  const order = async () => (await Promise.all(['Shell 1', 'Shell 2', 'Shell 3'].map(async (n) => [n, (await pane(n).boundingBox()).x]))).sort((a, b) => a[1] - b[1]).map((e) => e[0]);
  const previewOrder = async () => page.evaluate(() => [...document.querySelectorAll('.projection .leafmini')].sort((a, b) => a.getBoundingClientRect().left - b.getBoundingClientRect().left).map((e) => e.textContent));
  const before = await widths();
  const first = await pane('Shell 1').boundingBox();
  const y = first.y + first.height / 2;
  const seen = [];

  await drag(await center(pane('Shell 2').locator('.phead strong')), [{ x: first.x + first.width - 20, y }], { drop: false });
  assert.equal(await page.locator('.projection').count(), 0, 'beside its own left neighbor nothing would change, so there is no preview');
  for (let x = first.x + first.width - 20; x >= first.x + 2; x -= 15) {
    await page.mouse.move(x, y, { steps: 2 });
    if (await page.locator('.projection').count()) seen.push((await previewOrder()).join(','));
  }
  assert.deepEqual([...new Set(seen)], ['Shell 2,Shell 1,Shell 3'], 'one preview all the way to the border');
  await page.screenshot({ path: `${output}docking-reorder.png` });
  await page.mouse.up();
  assert.deepEqual(await order(), ['Shell 2', 'Shell 1', 'Shell 3']);
  assert.deepEqual(await widths(), before, 'every pane keeps its width');
});

test('dragging a pane header moves one view', async () => {
  const from = await center(pane('Tests').locator('.phead strong'));
  const bar = await page.locator('.tabbar').boundingBox();
  await drag(from, [{ x: bar.x + bar.width - 40, y: bar.y + bar.height / 2 }]);
  assert.deepEqual(await tabLabels(), ['Codex', 'Logs', 'Monitor', 'Tests']);
});

test('a drop does not select the tab under the pointer, and the next click works', async () => {
  const from = await center(pane('Tests').locator('.phead strong'));
  const monitor = await center(page.locator('.tab').nth(2));
  await drag(from, [{ x: monitor.x + monitor.width / 2 - 2, y: monitor.y }]);
  assert.deepEqual(await tabLabels(), ['Codex', 'Logs', 'Monitor', 'Tests']);
  assert.equal(await page.locator('.tab.on .tablabel').textContent(), 'Tests', 'the release did not click the Monitor tab');
  await page.locator('.tab-name').nth(1).click();
  assert.equal(await page.locator('.tab.on .tablabel').textContent(), 'Logs', 'an immediate click is not swallowed');
});

test('dragging a tab between titles reorders it', async () => {
  const from = await center(page.locator('.tab-name').nth(2));
  const first = await page.locator('.tab').nth(0).boundingBox();
  await drag(from, [{ x: first.x + 3, y: first.y + first.height / 2 }], { drop: false });
  assert.equal(await page.locator('.tab.ins').count(), 1, 'an insertion line shows the position');
  await page.mouse.up();
  assert.deepEqual(await tabLabels(), ['Monitor', 'Codex + Tests', 'Logs']);
});

test('cancelling a drag restores layout, selection and focus', async () => {
  const cases = [
    { name: 'Escape', end: async () => { await page.keyboard.press('Escape'); await page.mouse.up(); } },
    { name: 'a release outside the workspace and tab bar', end: async () => { const f = await center(page.locator('footer')); await page.mouse.move(f.x, f.y, { steps: 4 }); await page.mouse.up(); } },
  ];
  for (const c of cases) {
    {
      await pane('Codex').locator('input').focus();
      const from = await center(page.locator('.tab-name').nth(0));
      const logsTab = await center(page.locator('.tab').nth(1));
      const stage = await page.locator('.stage-inner').boundingBox();
      await drag(from, [rest(logsTab), { x: logsTab.x, y: stage.y + 80 }], { drop: false });
      assert.equal(await page.locator('.projection').count(), 1);
      await c.end();
      assert.equal(await page.locator('.projection').count(), 0);
      assert.deepEqual(await tabLabels(), ['Codex + Tests', 'Logs', 'Monitor']);
      assert.deepEqual(await shownNames(), ['Codex', 'Tests']);
      assert.equal(await page.evaluate(() => document.activeElement?.getAttribute('aria-label')), 'Command in Codex', c.name);
    }
  }
});

test('over the source itself there is no preview, and a release there changes nothing', async () => {
  await paneAction('Tests', 'Split below');
  const from = await center(page.locator('.gin b'));
  const inside = await center(pane('Shell 1').locator('.body'));
  await drag(from, [inside], { drop: false });
  assert.equal(await page.locator('.projection').count(), 0, 'no preview and no error over the moving group');
  assert.deepEqual(await page.locator('.pane.carried').evaluateAll((els) => els.map((e) => e.getAttribute('aria-label'))), ['Tests', 'Shell 1'], 'the carried panes are marked');
  const codex = await center(pane('Codex').locator('.body'));
  await page.mouse.move(codex.x - codex.width / 2 + 60, codex.y, { steps: 8 });
  assert.equal(await page.locator('.projection:not(.invalid)').count(), 1, 'a target outside the group previews as usual');
  await page.screenshot({ path: `${output}docking-carried.png` });
  await page.mouse.move(inside.x, inside.y, { steps: 8 });
  assert.equal(await page.locator('.projection').count(), 0);
  await page.mouse.up();
  assert.equal(await page.locator('.pane.carried').count(), 0);
  assert.deepEqual(await shownNames(), ['Codex', 'Tests', 'Shell 1']);
  assert.equal(await page.locator('.gin').count(), 1, 'the layout is unchanged');
});

test('dragging a pane shows no message at the start of the drag', async () => {
  const from = await center(pane('Tests').locator('.phead strong'));
  await drag(from, [{ x: from.x + 40, y: from.y + 60 }], { drop: false });
  assert.equal(await page.locator('.projection').count(), 0);
  assert.equal(await pane('Tests').evaluate((el) => el.classList.contains('carried')), true);
  await page.mouse.up();
});

test('the move dialog reaches the same layouts from the keyboard', async () => {
  await page.locator('.tabmenu').nth(0).focus();
  await page.keyboard.press('Enter');
  await page.keyboard.press('Enter'); // "Move tab…" is the first item and has focus
  const dialog = page.locator('dialog');
  assert.equal(await dialog.evaluate((d) => d.open), true);
  await dialog.locator('select').selectOption({ label: 'Whole tab · Logs' });
  await dialog.getByRole('button', { name: 'Above', exact: true }).click();
  assert.deepEqual((await dialog.locator('.leafmini').allTextContents()).sort(), ['Codex', 'Logs', 'Tests']);
  await dialog.getByRole('button', { name: 'Move', exact: true }).click();
  assert.equal(await dialog.evaluate((d) => d.open), false);
  assert.deepEqual(await tabLabels(), ['Codex + 2', 'Monitor']);
  const [codex, tests, logs] = await Promise.all(['Codex', 'Tests', 'Logs'].map((n) => pane(n).boundingBox()));
  assert.ok(codex.x < tests.x && codex.y === tests.y && logs.y > codex.y, 'matches the top-edge drag');
  assert.equal(await page.evaluate(() => document.activeElement?.closest('.pane')?.getAttribute('aria-label')), 'Codex', 'focus follows the moved group');
});

test('the move dialog disables Move for an unavailable target', async () => {
  await page.setViewportSize({ width: 340, height: 400 });
  await page.waitForTimeout(100);
  await tabAction(1, 'Move tab…');
  const dialog = page.locator('dialog');
  await dialog.locator('select').selectOption({ label: 'Whole tab · Codex + Tests' });
  assert.match(await dialog.locator('.note').textContent(), /Too little space/);
  assert.equal(await dialog.getByRole('button', { name: 'Move', exact: true }).isDisabled(), true);
  await page.keyboard.press('Escape');
  assert.equal(await dialog.evaluate((d) => d.open), false);
});

test('equalize shares the parent and keeps nested proportions', async () => {
  await paneAction('Tests', 'Split below');
  const dividers = page.locator('.divider');
  const outer = await center(dividers.nth(0));
  await page.mouse.move(outer.x, outer.y); await page.mouse.down(); await page.mouse.move(outer.x + 250, outer.y, { steps: 5 }); await page.mouse.up();
  const nested = await center(page.locator('.divider.rows'));
  await page.mouse.move(nested.x, nested.y); await page.mouse.down(); await page.mouse.move(nested.x, nested.y + 150, { steps: 5 }); await page.mouse.up();
  const ratio = async () => { const [a, b] = [await pane('Tests').boundingBox(), await pane('Shell 1').boundingBox()]; return a.height / b.height; };
  const beforeRatio = await ratio();
  await tabAction(0, 'Equalize sizes');
  const [codex, tests] = [await pane('Codex').boundingBox(), await page.locator('.frame:not(.root)').boundingBox()];
  assert.ok(Math.abs(codex.width - tests.width) < 2, `the parent's children share the space: ${codex.width} vs ${tests.width}`);
  assert.ok(Math.abs((await ratio()) - beforeRatio) < 0.01, 'the nested proportions survive');
});

test('menus are keyboard operable and return focus', async () => {
  const more = pane('Codex').locator('.more');
  await more.focus();
  await page.keyboard.press('Enter');
  assert.equal(await page.evaluate(() => document.activeElement?.textContent), 'Split right');
  await page.keyboard.press('ArrowDown');
  assert.equal(await page.evaluate(() => document.activeElement?.textContent), 'Split below');
  await page.keyboard.press('End');
  assert.equal(await page.evaluate(() => document.activeElement?.textContent), 'Close pane');
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('.pop').count(), 0);
  assert.equal(await more.evaluate((el) => el === document.activeElement), true);
});

test('keyboard focus selects a pane', async () => {
  await pane('Tests').locator('input').focus();
  assert.equal(await page.locator('.pane.focus').getAttribute('aria-label'), 'Tests');
});

test('tabs: + opens a session, the menu opens other types, the last close leaves the empty state', async () => {
  await page.getByRole('button', { name: 'New Session tab' }).click();
  assert.deepEqual(await tabLabels(), ['Codex + Tests', 'Logs', 'Monitor', 'Shell 1']);
  await page.getByRole('button', { name: 'Open another kind of tab' }).click();
  await page.getByRole('menuitem', { name: 'Logs', exact: true }).click();
  assert.equal((await tabLabels()).at(-1), 'Logs');
  for (let i = 0; i < 5; i++) await tabAction(0, 'Close tab');
  assert.equal(await page.locator('.empty').isVisible(), true);
  await page.getByRole('button', { name: 'Open a session' }).click();
  assert.deepEqual(await tabLabels(), ['Shell 1']);
});

test('a reload restores the committed layout and nothing temporary', async () => {
  await paneAction('Tests', 'Split below');
  const divider = await center(page.locator('.divider.cols'));
  await page.mouse.move(divider.x, divider.y); await page.mouse.down(); await page.mouse.move(divider.x - 150, divider.y, { steps: 5 }); await page.mouse.up();
  const before = await Promise.all(['Codex', 'Tests', 'Shell 1'].map((n) => pane(n).boundingBox()));
  await pane('Shell 1').locator('.zoom-toggle').click();
  // Leave zoom on and a hover preview on screen while the page reloads.
  const from = await center(page.locator('.tab-name').nth(1));
  const stage = await page.locator('.stage-inner').boundingBox();
  await drag(from, [{ x: stage.x + stage.width - 10, y: stage.y + stage.height / 2 }], { drop: false });
  assert.equal(await page.locator('.projection').count(), 1);
  await page.reload();
  await page.waitForSelector('.pane');
  assert.deepEqual(await tabLabels(), ['Codex + 2', 'Logs', 'Monitor'], 'the hover preview was not saved');
  assert.deepEqual(await Promise.all(['Codex', 'Tests', 'Shell 1'].map((n) => pane(n).boundingBox())), before, 'tree and weights are restored');
  assert.equal(await page.locator('.pane.focus').getAttribute('aria-label'), 'Shell 1', 'last focus is restored');
  assert.equal(await pane('Shell 1').locator('.zoom-toggle').textContent(), 'Zoom', 'zoom was not saved');
});
