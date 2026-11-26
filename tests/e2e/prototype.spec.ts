import { expect, type Locator, type Page, test } from '@playwright/test';

test('линия этапов соединяет отметки только в пределах строки', async ({ page }) => {
  await page.goto('/?fixture=measuring');
  const stages = page.getByRole('complementary', { name: 'Этапы испытания' });
  await expect(stages).toBeVisible();
  for (const width of [1920, 1405, 1200, 960, 560, 400]) {
    await page.setViewportSize({ width, height: 900 });
    const wrongConnections = await stages.getByRole('listitem').evaluateAll((items) =>
      items.flatMap((item, index) => {
        const next = items[index + 1];
        const hasNextOnRow =
          next !== undefined &&
          next.getBoundingClientRect().top === item.getBoundingClientRect().top;
        const line = getComputedStyle(item, '::before');
        const hasLine = line.content !== 'none' && line.display !== 'none';
        return hasLine === hasNextOnRow ? [] : [index + 1];
      }),
    );
    expect(wrongConnections, `Ширина окна ${width} px`).toEqual([]);
  }
});

async function captureAxisText(page: Page) {
  await page.addInitScript(() => {
    const drawText = CanvasRenderingContext2D.prototype.fillText;
    CanvasRenderingContext2D.prototype.fillText = function (text, x, y, maxWidth?) {
      this.canvas.dataset['axisText'] = `${this.canvas.dataset['axisText'] ?? ''}\u001f${text}`;
      if (maxWidth === undefined) drawText.call(this, text, x, y);
      else drawText.call(this, text, x, y, maxWidth);
    };
  });
}

async function leakageMarker(canvas: Locator) {
  return canvas.evaluate((element) => {
    if (!(element instanceof HTMLCanvasElement)) throw new Error('Canvas expected');
    const plot = element.closest('.u-wrap')?.querySelector('.u-over')?.getBoundingClientRect();
    const context = element.getContext('2d');
    if (!plot || !context) throw new Error('Chart is unavailable');
    const bounds = element.getBoundingClientRect();
    const scale = element.width / bounds.width;
    const x = (plot.left - bounds.left + (plot.width * (Math.log10(20) + 1)) / 5) * scale;
    const y = (plot.bottom - bounds.top - (plot.height * (Math.log10(10_010) - 1)) / 4) * scale;
    const accent = getComputedStyle(element).getPropertyValue('--accent').trim();
    const red = Number.parseInt(accent.slice(1, 3), 16);
    const green = Number.parseInt(accent.slice(3, 5), 16);
    const blue = Number.parseInt(accent.slice(5, 7), 16);
    const { data } = context.getImageData(0, 0, element.width, element.height);
    let near = 0;
    let elsewhere = 0;
    for (let index = 0; index < data.length; index += 4) {
      if (data[index] !== red || data[index + 1] !== green || data[index + 2] !== blue) continue;
      const pixel = index / 4;
      if (
        Math.hypot((pixel % element.width) - x, Math.floor(pixel / element.width) - y) <
        7 * scale
      ) {
        near++;
      } else {
        elsewhere++;
      }
    }
    return { near, elsewhere };
  });
}

async function captureBoundaryPaths(page: Page) {
  await page.addInitScript(() => {
    type Vertex = [number, number];
    const active = new WeakMap<CanvasRenderingContext2D, Vertex[]>();
    const painted = new WeakMap<HTMLCanvasElement, Vertex[][]>();
    const context = CanvasRenderingContext2D.prototype;
    const { beginPath, moveTo, lineTo, stroke } = context;
    context.beginPath = function () {
      active.set(this, []);
      beginPath.call(this);
    };
    context.moveTo = function (x, y) {
      active.get(this)?.push([x, y]);
      moveTo.call(this, x, y);
    };
    context.lineTo = function (x, y) {
      active.get(this)?.push([x, y]);
      lineTo.call(this, x, y);
    };
    context.stroke = function (path?: Path2D) {
      const vertices = active.get(this);
      if (!path && vertices?.length === 2 && this.lineWidth === 1.5 * devicePixelRatio) {
        const lines = [...(painted.get(this.canvas) ?? []), vertices].slice(-4);
        painted.set(this.canvas, lines);
        this.canvas.dataset['boundaryPaths'] = JSON.stringify(lines);
      }
      Reflect.apply(stroke, this, path ? [path] : []);
    };
  });
}

async function expectBoundariesAtTop(canvas: Locator, maxPressure: number) {
  await expect(canvas).toHaveAttribute('data-boundary-paths', /./);
  const paths = await canvas.evaluate((element) => {
    const plot = element.closest('.u-wrap')?.querySelector('.u-over')?.getBoundingClientRect();
    if (!(element instanceof HTMLCanvasElement) || !plot) throw new Error('Chart is unavailable');
    const bounds = element.getBoundingClientRect();
    const ratio = element.width / bounds.width;
    const x = (position: number) => (position / ratio + bounds.left - plot.left) / plot.width;
    const y = (position: number) => (position / ratio + bounds.top - plot.top) / plot.height;
    const lines: [[number, number], [number, number]][] = JSON.parse(
      element.dataset['boundaryPaths'] ?? '[]',
    );
    return {
      height: bounds.height,
      lines: lines.map(([[x0, y0], [x1, y1]]) => [
        [x(x0), y(y0)],
        [x(x1), y(y1)],
      ]),
    };
  });
  expect(paths.height).toBeGreaterThanOrEqual(550);
  expect(paths.lines).toHaveLength(4);
  for (const [index, coefficient] of [15, 3, 0.6, 0.12].entries()) {
    const points = paths.lines[index];
    if (!points) throw new Error('Boundary missing');
    const expected = [
      [(Math.log10(coefficient) + 0.56 + 1) / 5, 1],
      [(Math.log10(coefficient) + 0.56 * Math.log10(maxPressure) + 1) / 5, 0],
    ];
    for (const [endpoint, point] of points.entries()) {
      expect(point[0]).toBeCloseTo(expected[endpoint]?.[0] ?? -1, 2);
      expect(point[1]).toBeCloseTo(expected[endpoint]?.[1] ?? -1, 2);
    }
  }
}

test('четыре справочные линии доходят до верха графика без точки', async ({ page }) => {
  await captureBoundaryPaths(page);
  await page.goto('/?fixture=waiting&step=7');
  const canvas = page.getByRole('region', { name: 'Характеристика утечки' }).locator('canvas');
  await expectBoundariesAtTop(canvas, 10_000);
});

for (const viewport of [
  { width: 1366, height: 768 },
  { width: 1920, height: 1080 },
]) {
  test.describe(`прототип ${viewport.width}×${viewport.height}`, () => {
    test.use({ viewport });

    test('оператор проходит пять экранов и видит настоящий график', async ({ page }) => {
      const errors: string[] = [];
      await captureBoundaryPaths(page);
      page.on('pageerror', (error) => errors.push(error.message));
      await captureAxisText(page);
      await page.goto('/?fixture=empty');
      await expect(page.getByRole('heading', { name: 'Испытания', exact: true })).toBeVisible();
      await page.getByRole('button', { name: 'Новое испытание' }).first().click();
      await expect(page.getByRole('heading', { name: 'Параметры испытания' })).toBeVisible();
      await expect(
        page.getByRole('textbox', { name: 'Площадь клапана, квадратные метры' }),
      ).toBeVisible();
      await expect(page.locator('label:has(#parameter-area) sup')).toHaveText('2');
      await expect(page.locator('label:has(#parameter-pmax) sub')).toHaveText('max');
      await page.evaluate(async () => {
        await document.fonts.ready;
      });
      const typography = await page.locator('label:has(#parameter-pmax) sub').evaluate((index) => ({
        font: getComputedStyle(index).fontFamily,
        loaded: document.fonts.check('14px "Medeo Sans"', 'Pmax'),
        faceLoaded: [...document.fonts].some(
          (face) => face.family === 'Medeo Sans' && face.status === 'loaded',
        ),
        lowered: Number.parseFloat(getComputedStyle(index).bottom) < 0,
      }));
      expect(typography.font).toContain('Medeo Sans');
      expect(typography.loaded).toBe(true);
      expect(typography.faceLoaded).toBe(true);
      expect(typography.lowered).toBe(true);
      const raised = await page
        .locator('label:has(#parameter-area) sup')
        .evaluate((index) => Number.parseFloat(getComputedStyle(index).top) < 0);
      expect(raised).toBe(true);
      await page
        .getByRole('textbox', { name: 'Обозначение или наименование клапана' })
        .fill('Клапан для показа');
      await page.getByLabel('Число повторов для каждой стабилизации').fill('1');
      await page.getByRole('button', { name: 'Проверить готовность' }).click();
      await expect(page.getByRole('heading', { name: 'Готовность', exact: true })).toBeVisible();
      const start = page.getByRole('button', { name: 'Перейти к проведению' });
      await expect(start).toBeDisabled();
      await page.getByRole('checkbox', { name: /Клапан закрыт/ }).check();
      await page.getByRole('checkbox', { name: /Подключение соответствует направлению/ }).check();
      await start.click();
      await expect(page.getByRole('heading', { name: 'Проведение испытания' })).toBeVisible();
      await expect(page.getByText('Клапан для показа')).toBeVisible();
      const characteristic = page.getByRole('region', { name: 'Характеристика утечки' });
      const canvas = characteristic.locator('canvas');
      await expect(canvas).toBeVisible();
      await expect(page.getByRole('button', { name: 'Остановить испытание' })).toBeVisible();
      const longAction = page.getByRole('button', { name: 'Подтверждаю завершение серии' });
      const labelFits = await longAction.evaluate((button) => {
        const label = button.querySelector('span');
        if (!label) return false;
        const outer = button.getBoundingClientRect();
        const inner = label.getBoundingClientRect();
        return (
          inner.left >= outer.left &&
          inner.right <= outer.right &&
          inner.top >= outer.top &&
          inner.bottom <= outer.bottom &&
          label.scrollWidth <= label.clientWidth
        );
      });
      expect(labelFits, 'Длинная подпись помещается в кнопку').toBe(true);
      await page.evaluate(() => window.scrollTo(0, 0));
      const toolbar = await page.locator('.fixture-toolbar').boundingBox();
      expect(toolbar).not.toBeNull();
      if (!toolbar) throw new Error('Fixture toolbar is not visible');
      for (const selector of [
        '.main-reading strong',
        '.target-reading strong',
        '.differential-reading strong',
        '.action-panel h2',
        '.action-instruction',
        '.stop-button',
      ]) {
        const element = page.locator(selector);
        const box = await element.boundingBox();
        expect(box, selector).not.toBeNull();
        if (!box) throw new Error(`Element is not visible: ${selector}`);
        expect(box.x, selector).toBeGreaterThanOrEqual(0);
        expect(box.y, selector).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width, selector).toBeLessThanOrEqual(viewport.width);
        expect(box.y + box.height, selector).toBeLessThanOrEqual(viewport.height);
        const intersectsToolbar =
          box.x < toolbar.x + toolbar.width &&
          box.x + box.width > toolbar.x &&
          box.y < toolbar.y + toolbar.height &&
          box.y + box.height > toolbar.y;
        expect(intersectsToolbar, `Element overlaps fixture toolbar: ${selector}`).toBe(false);
        const visibleAtCenter = await element.evaluate((node) => {
          const bounds = node.getBoundingClientRect();
          const hit = document.elementFromPoint(
            bounds.left + bounds.width / 2,
            bounds.top + bounds.height / 2,
          );
          return hit !== null && (node === hit || node.contains(hit));
        });
        expect(visibleAtCenter, `Element obscured at center: ${selector}`).toBe(true);
      }
      await canvas.scrollIntoViewIfNeeded();
      await expect(canvas).toBeInViewport({ ratio: 0.98 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      );
      for (const action of [
        'Подтверждаю завершение серии',
        'Показать стабилизацию',
        'Показать следующий этап',
        'Подтверждаю открытие',
        'Подтверждаю закрытие',
        'Показать измерение',
      ]) {
        await page.getByRole('button', { name: action }).click();
      }
      await expect(characteristic).toContainText('Рабочая точка ещё не получена');
      await expect(characteristic.getByText('EN 1751:2024', { exact: false })).toHaveCount(0);
      await expect(characteristic.getByRole('listitem')).toHaveCount(4);
      expect(await leakageMarker(canvas)).toEqual({ near: 0, elsewhere: 0 });
      await expect(page.getByRole('button', { name: 'Остановить испытание' })).toBeEnabled();
      await expect(page.getByText('Подробности измерений', { exact: true })).toHaveCount(0);
      await expect(page.getByText('Таблица показаний', { exact: true })).toHaveCount(0);
      await expect(page.locator('.chart-panel canvas')).toHaveCount(1);
      await page.getByRole('button', { name: 'Показать снижение давления' }).click();
      await page.getByRole('button', { name: 'Показать результат' }).click();
      await expect(page.getByRole('heading', { name: 'Результат испытания' })).toBeVisible();
      const resultPage = page.getByRole('region', { name: 'Результат испытания' });
      const expectResultActionsDuringScroll = async () => {
        const actions = resultPage.locator('.work-actions');
        for (const progress of [0, 0.5, 1]) {
          await page.evaluate(
            (fraction) =>
              window.scrollTo(0, (document.documentElement.scrollHeight - innerHeight) * fraction),
            progress,
          );
          await expect(actions).toBeInViewport({ ratio: 1 });
          expect(
            await actions.evaluate((bar) =>
              Array.from(bar.querySelectorAll('button')).every((button) => {
                const { x, y, width, height } = button.getBoundingClientRect();
                return button.contains(document.elementFromPoint(x + width / 2, y + height / 2));
              }),
            ),
          ).toBe(true);
        }
      };
      const expectChartAboveParameters = () =>
        expect
          .poll(() =>
            characteristic.evaluate((chart) => {
              const page = chart.closest('.result-page');
              const overview = page?.querySelector('.result-overview')?.getBoundingClientRect();
              const parameters = page?.querySelector('.result-layout')?.getBoundingClientRect();
              const chartBounds = chart.getBoundingClientRect();
              return (
                !!overview &&
                !!parameters &&
                chartBounds.bottom < parameters.top &&
                parameters.bottom < overview.top &&
                page
                  ?.querySelector('.work-actions')
                  ?.previousElementSibling?.classList.contains('result-overview')
              );
            }),
          )
          .toBe(true);
      const expectResultSectionSpacing = () =>
        expect
          .poll(() =>
            resultPage.evaluate((page) => {
              return Array.from(page.children)
                .slice(1)
                .map(
                  (section) =>
                    section.getBoundingClientRect().top -
                    (section.previousElementSibling?.getBoundingClientRect().bottom ?? 0),
                );
            }),
          )
          .toEqual([18, 18, 18, 18]);
      await expectChartAboveParameters();
      await expectResultActionsDuringScroll();
      await expect(resultPage.locator('.result-overview')).toBeInViewport({ ratio: 1 });
      await expectResultSectionSpacing();
      await expect(characteristic.getByText('20 л/(с·м²)')).toBeVisible();
      await expect(characteristic.getByText('10 010 Па')).toBeVisible();
      await expect(canvas).toHaveAttribute('data-axis-text', /Удельная утечка, л\/\(с·м²\)/);
      await expect(canvas).toHaveAttribute('data-axis-text', /Испытательное давление, Па/);
      const axisText = (await canvas.getAttribute('data-axis-text'))?.split('\u001f') ?? [];
      expect(axisText).toEqual(expect.arrayContaining(['1', '10', '100', '1 000']));
      expect(axisText).not.toContain('0');
      await expectBoundariesAtTop(canvas, 100_000);
      const marker = await leakageMarker(canvas);
      expect(marker.near).toBeGreaterThan(0);
      expect(marker.elsewhere).toBe(0);
      const canvasWidth = await canvas.getAttribute('width');
      await page.setViewportSize({ width: 819, height: 768 });
      await expect(canvas).not.toHaveAttribute('width', canvasWidth ?? '');
      await expectChartAboveParameters();
      await expectResultActionsDuringScroll();
      await expectResultSectionSpacing();
      const resized = await leakageMarker(canvas);
      expect(resized.near).toBeGreaterThan(0);
      expect(resized.elsewhere).toBe(0);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      );

      await page.getByRole('button', { name: 'К списку' }).click();
      await page.getByRole('button', { name: 'Результат прохода' }).click();
      await expect(characteristic.getByText('20 л/(с·м²)')).toBeVisible();
      await page.getByRole('button', { name: 'К списку' }).click();
      await page.getByRole('button', { name: /Открыть Клапан для показа/ }).click();
      await expect(page.getByRole('heading', { name: 'Результат испытания' })).toBeVisible();
      await expect(characteristic).toContainText('Рабочая точка недоступна');

      await page.goto('/?fixture=save-error');
      await expect(page.getByText('Сохранение не подтверждено')).toBeVisible();
      await expect(characteristic).toContainText('Рабочая точка недоступна');
      expect(await leakageMarker(canvas)).toEqual({ near: 0, elsewhere: 0 });
      expect(errors).toEqual([]);
    });

    test('состояние потери ДД1 воспроизводимо по прямой ссылке', async ({ page }) => {
      await page.goto('/?fixture=sensor-lost&step=7');
      await expect(page.getByRole('heading', { name: 'Проведение испытания' })).toBeVisible();
      await expect(page.locator('.main-reading strong')).toHaveText('—');
      await expect(page.locator('.main-reading')).toContainText('устарело');
      await expect(
        page.getByText('Данные ДД1 недоступны. Текущее давление неизвестно.'),
      ).toBeVisible();
      await page.reload();
      await expect(page.locator('.main-reading strong')).toHaveText('—');
      await page.getByRole('button', { name: 'Остановить испытание' }).click();
      await expect(page).toHaveURL(/sensor=lost/);
      await expect(page.locator('.main-reading strong')).toHaveText('—');
      await page.reload();
      await expect(page.getByRole('button', { name: 'Остановка выполняется' })).toBeDisabled();
      await expect(page.locator('.main-reading strong')).toHaveText('—');
      await expect(page.locator('.main-reading')).toContainText('устарело');
    });

    test('выбранный период усреднения виден на протяжении примера', async ({ page }) => {
      await page.goto('/?fixture=empty');
      await page.getByRole('button', { name: 'Новое испытание' }).click();
      await page
        .getByRole('textbox', { name: 'Обозначение или наименование клапана' })
        .fill('Клапан 100');
      await page.getByRole('textbox', { name: 'Длительность измерения, с' }).fill('99');
      await page.getByLabel('Число повторов для каждой стабилизации').fill('1');
      await page.getByRole('button', { name: 'Проверить готовность' }).click();
      await page.getByRole('checkbox', { name: /Клапан закрыт/ }).check();
      await page.getByRole('checkbox', { name: /Подключение соответствует направлению/ }).check();
      await page.getByRole('button', { name: 'Перейти к проведению' }).click();
      for (const action of [
        'Подтверждаю завершение серии',
        'Показать стабилизацию',
        'Показать следующий этап',
        'Подтверждаю открытие',
        'Подтверждаю закрытие',
        'Показать измерение',
      ]) {
        await page.getByRole('button', { name: action }).click();
      }
      await expect(page.getByText('Прошло 49 с · осталось 50 с')).toBeVisible();
    });

    test('остановка сохраняет подготовленные показания по умолчанию после обновления', async ({
      page,
    }) => {
      await page.goto('/?fixture=actuator');
      await page.getByRole('button', { name: 'Остановить испытание' }).click();
      await expect(page.locator('.main-reading strong')).toHaveText('—');
      await expect(page.locator('.differential-reading strong')).toHaveText('—');
      await page.reload();
      await expect(page.locator('.main-reading strong')).toHaveText('—');
      await expect(page.locator('.differential-reading strong')).toHaveText('—');
      await page.goto('/?fixture=waiting&step=7');
      await expect(page.locator('.main-reading strong')).toContainText('2 000');
      await page.getByRole('button', { name: 'Остановить испытание' }).click();
      await expect(page.locator('.main-reading strong')).toContainText('2 000');
      await page.reload();
      await expect(page.locator('.main-reading strong')).toContainText('2 000');
      await expect(page.getByRole('button', { name: 'Остановка выполняется' })).toBeDisabled();
      const characteristic = page.getByRole('region', { name: 'Характеристика утечки' });
      await expect(characteristic).toContainText('Рабочая точка недоступна');
      expect(await leakageMarker(characteristic.locator('canvas'))).toEqual({
        near: 0,
        elsewhere: 0,
      });
    });

    test('навигация сохраняет черновик и блокируется во время прохода', async ({ page }) => {
      const valve = 'К'.repeat(200);
      await page.goto('/?fixture=empty');
      await page.getByRole('button', { name: 'Новое испытание' }).click();
      await page.getByRole('textbox', { name: 'Обозначение или наименование клапана' }).fill(valve);
      await page.getByRole('textbox', { name: 'Площадь клапана, квадратные метры' }).fill('0,');
      const nav = page.getByRole('navigation', { name: 'Навигация' });
      await nav.getByRole('button', { name: 'Испытания' }).click();
      await nav.getByRole('button', { name: 'Параметры' }).click();
      await expect(
        page.getByRole('textbox', { name: 'Площадь клапана, квадратные метры' }),
      ).toHaveValue('0,');
      await page.getByRole('textbox', { name: 'Площадь клапана, квадратные метры' }).fill('0,12');
      await page.getByLabel('Число повторов для каждой стабилизации').fill('1');
      await page.getByRole('button', { name: 'Проверить готовность' }).click();
      await page.getByRole('checkbox', { name: /Клапан закрыт/ }).check();
      await page.getByRole('checkbox', { name: /Подключение соответствует направлению/ }).check();
      await page.getByRole('button', { name: 'Перейти к проведению' }).click();
      await page.getByRole('button', { name: 'Подтверждаю завершение серии' }).click();
      await expect(nav.getByRole('button', { name: 'Испытания' })).toBeDisabled();
      await expect(nav.getByRole('button', { name: 'Параметры' })).toBeDisabled();
      await expect(page.getByRole('heading', { name: 'Выход на рабочее давление' })).toBeVisible();
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1),
      ).toBe(true);
      await expect(page.getByRole('button', { name: 'Остановить испытание' })).toBeVisible();
    });
  });
}

test('обычные слова в истории остаются читаемыми после завершения прохода', async ({ page }) => {
  await page.setViewportSize({ width: 1186, height: 793 });
  await page.goto('/?fixture=empty');
  await page.getByRole('button', { name: 'Новое испытание' }).click();
  await page.getByLabel('Обозначение или наименование клапана').fill('Проверка Windows');
  await page.getByLabel('Число повторов для каждой стабилизации').fill('1');
  await page.getByRole('button', { name: 'Проверить готовность' }).click();
  await page.getByRole('checkbox', { name: /Клапан закрыт/ }).check();
  await page.getByRole('checkbox', { name: /Подключение соответствует направлению/ }).check();
  await page.getByRole('button', { name: 'Перейти к проведению' }).click();
  for (const name of [
    'Подтверждаю завершение серии',
    'Показать стабилизацию',
    'Показать следующий этап',
    'Подтверждаю открытие',
    'Подтверждаю закрытие',
    'Показать измерение',
    'Показать снижение давления',
    'Показать результат',
  ])
    await page.getByRole('button', { name }).click();
  await page.getByRole('button', { name: 'К списку' }).click();
  await page.evaluate(() => document.fonts.ready);
  const row = page.locator('.work-table tbody tr').filter({ hasText: 'Проверка Windows' });
  await expect(row).toBeVisible();
  await expect(row.getByText('Проверка Windows')).toBeVisible();
  const brokenWords = await row.locator('td').evaluateAll((cells) => {
    const broken: string[] = [];
    for (const cell of cells.slice(1, 7)) {
      const walker = document.createTreeWalker(cell, NodeFilter.SHOW_TEXT);
      while (walker.nextNode()) {
        const node = walker.currentNode;
        for (const word of (node.textContent ?? '').matchAll(/\S+/g)) {
          const range = document.createRange();
          range.setStart(node, word.index ?? 0);
          range.setEnd(node, (word.index ?? 0) + word[0].length);
          if (range.getClientRects().length > 1) broken.push(word[0]);
        }
      }
    }
    return broken;
  });
  expect(brokenWords).toEqual([]);
});

for (const fixture of ['readiness']) {
  test(`широкая история не расширяет страницу (${fixture})`, async ({ page }) => {
    await page.setViewportSize({ width: 819, height: 614 });
    await page.goto(`/?fixture=${fixture}`);
    await page.getByRole('button', { name: 'Испытания', exact: true }).click();
    const table = page.locator('.history-table');
    await expect(table.locator('tbody tr').first()).toBeVisible();
    await page.evaluate(() => document.fonts.ready);
    for (const width of [1534, 819]) {
      await page.setViewportSize({ width, height: 893 });
      const alignment = await page.locator('.history-surface').evaluate((surface) => {
        const left = (selector: string) => {
          const child = surface.querySelector(selector);
          if (!child) throw new Error(`Missing history element: ${selector}`);
          return child.getBoundingClientRect().left;
        };
        const card = surface.getBoundingClientRect().left;
        return {
          inset: left('.history-outcome-tabs button') - card,
          heading: left('h2') - card,
          search: left('.history-search') - card,
          table: left('.table-scroll') - card,
        };
      });
      expect(alignment.inset).toBeGreaterThan(8);
      expect(alignment.inset).toBeCloseTo(alignment.heading, 0);
      expect(alignment.inset).toBeCloseTo(alignment.search, 0);
      expect(alignment.table).toBeCloseTo(1, 0);
    }
    const dimensions = await page.evaluate(() => ({
      viewport: document.documentElement.clientWidth,
      page: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
    }));
    expect(dimensions.page).toBeLessThanOrEqual(dimensions.viewport);
    const scroller = page.locator('.table-scroll');
    const scroll = await scroller.evaluate((node) => {
      node.scrollLeft = node.scrollWidth;
      return {
        left: node.scrollLeft,
        width: node.clientWidth,
        content: node.scrollWidth,
        overflow: getComputedStyle(node).overflowX,
      };
    });
    expect(scroll.content).toBeGreaterThan(scroll.width);
    expect(scroll.left).toBeGreaterThan(0);
    expect(['auto', 'scroll']).toContain(scroll.overflow);
    const button = table
      .locator('tbody tr')
      .first()
      .getByRole('button', {
        name: fixture === 'measuring' ? /Вернуться к испытанию/ : /Открыть/,
      });
    const inside = await button.evaluate((node) => {
      const box = node.getBoundingClientRect();
      const container = node.closest('.table-scroll')?.getBoundingClientRect();
      return (
        !!container &&
        box.left >= Math.max(0, container.left) &&
        box.right <= Math.min(document.documentElement.clientWidth, container.right)
      );
    });
    expect(inside).toBe(true);
    expect(
      await page.evaluate(() =>
        Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
      ),
    ).toBeLessThanOrEqual(dimensions.viewport);
  });
}
