import { expect, test } from '@playwright/test';

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    const text = CanvasRenderingContext2D.prototype.fillText;
    const clear = CanvasRenderingContext2D.prototype.clearRect;
    const stroke = CanvasRenderingContext2D.prototype.stroke;
    CanvasRenderingContext2D.prototype.clearRect = function (x, y, width, height) {
      this.canvas.dataset['axisText'] = '';
      this.canvas.dataset['strokes'] = '';
      clear.call(this, x, y, width, height);
    };
    CanvasRenderingContext2D.prototype.stroke = function (path?: Path2D) {
      this.canvas.dataset['strokes'] =
        `${this.canvas.dataset['strokes'] ?? ''}|${this.strokeStyle}:${this.getLineDash().join(',')}`;
      if (path) stroke.call(this, path);
      else Reflect.apply(stroke, this, []);
    };
    CanvasRenderingContext2D.prototype.fillText = function (value, x, y, maxWidth) {
      this.canvas.dataset['axisText'] = `${this.canvas.dataset['axisText'] ?? ''}\u001f${value}`;
      if (maxWidth === undefined) text.call(this, value, x, y);
      else text.call(this, value, x, y, maxWidth);
    };
  });
});

test('заданный интервал переживает double-click, а курсор обновляется при разрыве без смены шкал', async ({
  page,
}) => {
  await page.goto('/tests/e2e/chart.html');
  const canvas = page.locator('.chart-container canvas').first();
  await expect(canvas).toHaveAttribute('data-axis-text', /Перепад, Па/);
  const xLabels = () =>
    canvas.evaluate((element) => {
      const labels = (element.dataset['axisText'] ?? '').split('\u001f');
      const start = labels.lastIndexOf('Время, с');
      const end = labels.indexOf('Давление, Па', start);
      return labels.slice(start + 1, end);
    });
  expect(await xLabels()).toContain('60');
  const overlay = page.locator('.chart-container .u-over');
  await overlay.dblclick();
  await page.evaluate(() => new Promise(requestAnimationFrame));
  expect(await xLabels()).toContain('60');

  await page.getByText('Таблица показаний', { exact: true }).click();
  await expect(
    page.getByRole('table', { name: 'Подготовленные показания по времени' }),
  ).toBeVisible();
  await page.getByRole('button', { name: 'Разрыв', exact: true }).focus();
  const box = await overlay.boundingBox();
  if (!box) throw new Error('Plot overlay is missing');
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  const marker = page.locator('.chart-container .u-cursor-pt').first();
  await expect(marker).toBeVisible();
  const before = await marker.getAttribute('style');
  await page.keyboard.press('Enter');
  await expect(page.locator('.chart-data-table tbody tr').last()).toContainText('Нет данных');
  await expect.poll(() => marker.getAttribute('style')).not.toBe(before);
  await expect(marker).toBeHidden();
  await page.getByRole('button', { name: 'Авто', exact: true }).click();
  await expect.poll(xLabels).toContain('30');
  expect(await xLabels()).not.toContain('60');
});

test('тот же canvas обновляет физические шкалы, допуск, пропуски и размер после смены параметров', async ({
  page,
}) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.goto('/tests/e2e/chart.html');
  const canvas = page.locator('.chart-container canvas').first();
  await expect(canvas).toHaveAttribute('data-axis-text', /Перепад, Па/);
  const original = await canvas.elementHandle();
  if (!original) throw new Error('Chart canvas is missing');
  await page.getByText('Таблица показаний').click();
  const parse = (value: string) => Number(value.replace(/\s/g, '').replace(',', '.'));
  for (const example of [
    { button: 'Малый', target: 1e-100, range: 1e-100 },
    { button: 'Другой малый период', target: 1e-100, range: 2e-100 },
    // Числа для рисования совпадают; меняется только физический коэффициент давления.
    { button: 'Крупное давление', target: 1e200, range: 1e-100 },
    { button: 'Большой', target: 1e200, range: 1e307 },
    { button: 'Узкий', target: 1e-15, range: 60 },
    { button: 'Обычный', target: 10000, range: 60 },
    { button: 'Малый', target: 1e-100, range: 1e-100 },
  ]) {
    await page.getByRole('button', { name: example.button, exact: true }).click();
    await expect
      .poll(async () => {
        const painted = ((await canvas.getAttribute('data-axis-text')) ?? '').split('\u001f');
        const x = painted.lastIndexOf('Время, с');
        const pressure = painted.indexOf('Давление, Па', x);
        const differential = painted.indexOf('Перепад, Па', pressure);
        return [
          painted.slice(x + 1, pressure).map(parse),
          painted.slice(pressure + 1, differential).map(parse),
          painted.slice(differential + 1).map(parse),
        ];
      })
      .toEqual([
        expect.arrayContaining([0, example.range]),
        expect.arrayContaining([example.target]),
        expect.arrayContaining([80]),
      ]);
    const axes = await canvas.evaluate((element) => {
      const text = (element.dataset['axisText'] ?? '').split('\u001f');
      const x = text.lastIndexOf('Время, с');
      const pressure = text.indexOf('Давление, Па', x);
      const differential = text.indexOf('Перепад, Па', pressure);
      return [
        text.slice(x + 1, pressure),
        text.slice(pressure + 1, differential),
        text.slice(differential + 1),
      ];
    });
    for (const labels of axes) {
      expect(labels.length).toBeGreaterThan(1);
      expect(labels.map(parse).every(Number.isFinite)).toBe(true);
      expect(new Set(labels).size).toBe(labels.length);
      expect(new Set(labels.map(parse)).size).toBe(labels.length);
    }
    const rows = page.locator('.chart-data-table tbody tr');
    const last = rows.last().locator('td');
    expect(parse((await last.nth(0).textContent()) ?? '')).toBe(example.range / 2);
    const expectedPressure = example.button === 'Обычный' ? 10010 : example.target * 1.001;
    expect(parse((await last.nth(1).textContent()) ?? '') / expectedPressure).toBeCloseTo(1, 12);
    if (example.button !== 'Обычный') {
      await expect(rows.nth(2)).toContainText('Нет данных');
      const pixels = await canvas.evaluate((element) => {
        if (!(element instanceof HTMLCanvasElement)) throw new Error('Not a canvas');
        const overlay = element.closest('.uplot')?.querySelector('.u-over');
        if (!overlay) throw new Error('Missing plot overlay');
        const plot = overlay.getBoundingClientRect();
        const bounds = element.getBoundingClientRect();
        const ratio = element.width / bounds.width;
        const image = element.getContext('2d')?.getImageData(0, 0, element.width, element.height);
        if (!image) throw new Error('Canvas pixels unavailable');
        const count = (from: number, to: number) => {
          let matching = 0;
          for (
            let y = Math.ceil((plot.top - bounds.top) * ratio) + 2;
            y < (plot.bottom - bounds.top) * ratio - 2;
            y++
          ) {
            for (
              let x = Math.ceil((plot.left - bounds.left + plot.width * from) * ratio);
              x < (plot.left - bounds.left + plot.width * to) * ratio;
              x++
            ) {
              const offset = (y * element.width + x) * 4;
              if (
                Math.abs((image.data[offset] ?? 0) - 39) < 8 &&
                Math.abs((image.data[offset + 1] ?? 0) - 109) < 8 &&
                Math.abs((image.data[offset + 2] ?? 0) - 104) < 8 &&
                (image.data[offset + 3] ?? 0) > 200
              )
                matching++;
            }
          }
          return matching;
        };
        return { segment: count(0.02, 0.12), gap: count(0.18, 0.32) };
      });
      expect(pixels.segment).toBeGreaterThan(0);
      expect(pixels.gap).toBe(0);
    }
    const strokes = ((await canvas.getAttribute('data-strokes')) ?? '').split('|');
    expect(strokes.filter((stroke) => stroke === '#985a26:2,5')).toHaveLength(2);
    expect(strokes).toContain('#526b65:2,4');
    expect(strokes).toContain('#985a26:7,3');
    expect(await canvas.evaluate((element, previous) => element === previous, original)).toBe(true);
    await expect(page.locator('.chart-legend')).toContainText(
      `Допуск ±${example.button === 'Обычный' ? 2 : 5}%`,
    );
  }
  const width = () =>
    canvas.evaluate((element) => {
      if (!(element instanceof HTMLCanvasElement)) throw new Error('Not a canvas');
      return element.width;
    });
  const before = await width();
  await page.setViewportSize({ width: 640, height: 800 });
  await expect.poll(width).not.toBe(before);
  expect(await canvas.evaluate((element, previous) => element === previous, original)).toBe(true);
  expect(await width()).toBeGreaterThan(0);
  await page.getByRole('button', { name: 'Пустой', exact: true }).click();
  await expect(page.locator('.chart-container .uplot')).toBeHidden();
  await expect(page.locator('.chart-data-table tbody tr')).toHaveCount(0);
  await page.getByRole('button', { name: 'Обычный', exact: true }).click();
  await expect(canvas).toBeVisible();
  expect(await canvas.evaluate((element, previous) => element === previous, original)).toBe(true);
  expect(errors).toEqual([]);
});
