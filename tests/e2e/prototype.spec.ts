import { expect, test } from '@playwright/test';

for (const viewport of [
  { width: 1366, height: 768 },
  { width: 1920, height: 1080 },
]) {
  test.describe(`прототип ${viewport.width}×${viewport.height}`, () => {
    test.use({ viewport });

    test('оператор проходит пять экранов и видит настоящий график', async ({ page }) => {
      const errors: string[] = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto('/?fixture=empty');
      await expect(page.getByText('Макет · подготовленные данные')).toBeVisible();
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
      await page.getByRole('button', { name: 'Проверить готовность' }).click();
      await expect(page.getByRole('heading', { name: 'Готовность', exact: true })).toBeVisible();
      const start = page.getByRole('button', { name: 'Перейти к проведению' });
      await expect(start).toBeDisabled();
      await page.getByRole('checkbox', { name: /Клапан закрыт/ }).check();
      await page.getByRole('checkbox', { name: /Подключение соответствует направлению/ }).check();
      await start.click();
      await expect(page.getByRole('heading', { name: 'Проведение испытания' })).toBeVisible();
      await expect(page.getByText('Клапан для показа')).toBeVisible();
      await expect(page.locator('.chart-container canvas').first()).toBeVisible();
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
        '.chart-container',
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
      for (const action of [
        'Подтверждаю завершение серии',
        'Показать стабилизацию',
        'Показать следующий этап',
        'Подтверждаю открытие',
        'Подтверждаю закрытие',
        'Показать измерение',
        'Показать снижение давления',
        'Показать результат',
      ]) {
        await page.getByRole('button', { name: action }).click();
      }
      await expect(page.getByRole('heading', { name: 'Результат испытания' })).toBeVisible();
      await expect(page.getByText('Не рассчитан')).toBeVisible();
      await expect(page.getByText('Не определён')).toBeVisible();
      expect(errors).toEqual([]);
    });

    test('состояние потери ДД1 воспроизводимо по прямой ссылке', async ({ page }) => {
      await page.goto('/?fixture=sensor-lost&step=7');
      await expect(page.getByRole('heading', { name: 'Проведение испытания' })).toBeVisible();
      await expect(page.locator('.main-reading strong')).toHaveText('Неизвестно');
      await expect(page.locator('.main-reading')).toContainText('устарело');
      await expect(
        page.getByText('Данные ДД1 недоступны. Текущее давление неизвестно.'),
      ).toBeVisible();
      await page.reload();
      await expect(page.locator('.main-reading strong')).toHaveText('Неизвестно');
      await page.getByRole('button', { name: 'Остановить испытание' }).click();
      await expect(page).toHaveURL(/sensor=lost/);
      await expect(page.locator('.main-reading strong')).toHaveText('Неизвестно');
      await page.reload();
      await expect(page.getByRole('button', { name: 'Остановка выполняется' })).toBeDisabled();
      await expect(page.locator('.main-reading strong')).toHaveText('Неизвестно');
      await expect(page.locator('.main-reading')).toContainText('устарело');
    });

    test('график и таблица показывают только доступные показания', async ({ page }) => {
      await page.goto('/?fixture=measuring');
      await expect(page.getByText('Ряд: 30 с · период усреднения утечки: 60 с')).toBeVisible();
      const legend = page.locator('.chart-legend');
      const chart = page.locator('.chart-container');
      const chartBounds = await chart.boundingBox();
      const legendBounds = await legend.boundingBox();
      expect(chartBounds).not.toBeNull();
      expect(legendBounds).not.toBeNull();
      if (!chartBounds || !legendBounds) throw new Error('Chart or legend is not visible');
      expect(legendBounds.y).toBeGreaterThanOrEqual(chartBounds.y + chartBounds.height);
      await page.getByText('Таблица показаний').click();
      const rows = page
        .getByRole('table', { name: 'Подготовленные показания по времени' })
        .getByRole('row');
      await expect(rows).toHaveCount(8);
      await expect(rows.last()).toContainText('30');
      await expect(rows.last()).toContainText('10 010');
      await expect(rows.last()).toContainText('83');

      await page.goto('/?fixture=sensor-lost');
      await page.getByText('Таблица показаний').click();
      const lostRows = page
        .getByRole('table', { name: 'Подготовленные показания по времени' })
        .getByRole('row');
      await expect(lostRows.last()).toContainText('Нет данных');
      await expect(page.locator('.main-reading')).toContainText('устарело');
    });

    test('выбранный период усреднения виден на протяжении примера', async ({ page }) => {
      await page.goto('/?fixture=empty');
      await page.getByRole('button', { name: 'Новое испытание' }).click();
      await page
        .getByRole('textbox', { name: 'Обозначение или наименование клапана' })
        .fill('Клапан 100');
      await page.getByRole('textbox', { name: 'Период усреднения утечки, с' }).fill('100');
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
      await expect(page.getByText('Ряд: 50 с · период усреднения утечки: 100 с')).toBeVisible();
      await expect(page.getByText('Пример: прошло 50 с · осталось 50 с')).toBeVisible();
      await page.getByText('Таблица показаний').click();
      const rows = page
        .getByRole('table', { name: 'Подготовленные показания по времени' })
        .getByRole('row');
      await expect(rows.last()).toContainText('50');
    });

    test('подписи осей сохраняют различимость малых и обычных величин', async ({ page }) => {
      const errors: string[] = [];
      page.on('pageerror', (error) => errors.push(error.message));
      // Захватываем текст, реально отрисованный uPlot в Canvas, а не внутренние данные графика.
      await page.addInitScript(() => {
        const drawText = CanvasRenderingContext2D.prototype.fillText;
        CanvasRenderingContext2D.prototype.fillText = function (
          this: CanvasRenderingContext2D,
          text: string,
          x: number,
          y: number,
          maxWidth?: number,
        ) {
          this.canvas.dataset['axisText'] = `${this.canvas.dataset['axisText'] ?? ''}\u001f${text}`;
          if (maxWidth === undefined) drawText.call(this, text, x, y);
          else drawText.call(this, text, x, y, maxWidth);
        };
      });

      for (const example of [
        { pressure: '10000', period: '60', expectedX: 60, expectedY: 10_000 },
        { pressure: '10000', period: '100', expectedX: 100, expectedY: 10_000 },
        { pressure: '0,0001', period: '0,0001', expectedX: 0.0001, expectedY: 0.0001 },
        { pressure: `0,${'0'.repeat(14)}1`, period: '60', expectedX: 60, expectedY: 1e-15 },
        { pressure: `0,${'0'.repeat(99)}1`, period: '60', expectedX: 60, expectedY: 1e-100 },
        { pressure: '10000', period: `0,${'0'.repeat(99)}1`, expectedX: 1e-100, expectedY: 10_000 },
        { pressure: '10000', period: `1${'0'.repeat(307)}`, expectedX: 1e307, expectedY: 10_000 },
      ]) {
        await page.goto('/?fixture=empty');
        await page.getByRole('button', { name: 'Новое испытание' }).click();
        await page
          .getByRole('textbox', { name: 'Обозначение или наименование клапана' })
          .fill('Клапан для проверки осей');
        await page.getByRole('textbox', { name: 'Pmax производителя, Па' }).fill(example.pressure);
        await page
          .getByRole('textbox', { name: 'Период усреднения утечки, с' })
          .fill(example.period);
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
        const canvas = page.locator('.chart-container canvas').first();
        await expect(canvas).toHaveAttribute('data-axis-text', /Перепад, Па/);
        const painted = (await canvas.getAttribute('data-axis-text'))?.split('\u001f') ?? [];
        const xStart = painted.lastIndexOf('Время, с');
        const yStart = painted.indexOf('Давление, Па', xStart);
        const yEnd = painted.indexOf('Перепад, Па', yStart);
        expect(xStart).toBeGreaterThanOrEqual(0);
        expect(yStart).toBeGreaterThan(xStart);
        expect(yEnd).toBeGreaterThan(yStart);
        const xLabels = painted.slice(xStart + 1, yStart);
        const yLabels = painted.slice(yStart + 1, yEnd);
        expect(xLabels.length).toBeGreaterThan(1);
        expect(yLabels.length).toBeGreaterThan(1);
        expect(new Set(xLabels).size).toBe(xLabels.length);
        expect(new Set(yLabels).size).toBe(yLabels.length);
        const parseLabel = (label: string) => Number(label.replace(/\s/g, '').replace(',', '.'));
        const xValues = xLabels.map(parseLabel);
        const yValues = yLabels.map(parseLabel);
        expect(xValues.every(Number.isFinite)).toBe(true);
        expect(yValues.every(Number.isFinite)).toBe(true);
        expect(new Set(xValues).size).toBe(xValues.length);
        expect(new Set(yValues).size).toBe(yValues.length);
        expect(xValues.filter((value) => value === 0)).toHaveLength(1);
        expect(xValues).toContain(example.expectedX);
        expect(yValues).toContain(example.expectedY);
        expect(yValues).not.toContain(0);
        await page.getByText('Таблица показаний').click();
        const lastTime = await page
          .locator('.chart-data-table tbody tr:last-child td:first-child')
          .textContent();
        expect(lastTime).not.toBeNull();
        if (lastTime === null) throw new Error('Последний момент времени отсутствует');
        expect(parseLabel(lastTime)).toBe(example.expectedX / 2);
        const lastPressure = await page
          .locator('.chart-data-table tbody tr:last-child td:nth-child(2)')
          .textContent();
        if (lastPressure === null) throw new Error('Последнее давление отсутствует');
        expect(Math.abs(parseLabel(lastPressure) / (example.expectedY * 1.001) - 1)).toBeLessThan(
          1e-12,
        );
      }
      expect(errors).toEqual([]);
    });

    test('остановка сохраняет подготовленные показания по умолчанию после обновления', async ({
      page,
    }) => {
      await page.goto('/?fixture=actuator');
      await page.getByRole('button', { name: 'Остановить испытание' }).click();
      await expect(page.locator('.main-reading strong')).toHaveText('Неизвестно');
      await expect(page.locator('.differential-reading strong')).toHaveText('Неизвестно');
      await page.reload();
      await expect(page.locator('.main-reading strong')).toHaveText('Неизвестно');
      await expect(page.locator('.differential-reading strong')).toHaveText('Неизвестно');
      await page.goto('/?fixture=waiting&step=7');
      await expect(page.locator('.main-reading strong')).toContainText('2 000');
      await page.getByRole('button', { name: 'Остановить испытание' }).click();
      await expect(page.locator('.main-reading strong')).toContainText('2 000');
      await page.reload();
      await expect(page.locator('.main-reading strong')).toContainText('2 000');
      await expect(page.getByRole('button', { name: 'Остановка выполняется' })).toBeDisabled();
    });

    test('навигация сохраняет черновик и возвращает к активному проходу', async ({ page }) => {
      await page.goto('/?fixture=empty');
      await page.getByRole('button', { name: 'Новое испытание' }).click();
      await page
        .getByRole('textbox', { name: 'Обозначение или наименование клапана' })
        .fill('Клапан навигации');
      await page.getByRole('textbox', { name: 'Площадь клапана, квадратные метры' }).fill('0,');
      const nav = page.getByRole('navigation', { name: 'Навигация' });
      await nav.getByRole('button', { name: 'Испытания' }).click();
      await nav.getByRole('button', { name: 'Параметры' }).click();
      await expect(
        page.getByRole('textbox', { name: 'Площадь клапана, квадратные метры' }),
      ).toHaveValue('0,');
      await page.getByRole('textbox', { name: 'Площадь клапана, квадратные метры' }).fill('0,12');
      await page.getByRole('button', { name: 'Проверить готовность' }).click();
      await page.getByRole('checkbox', { name: /Клапан закрыт/ }).check();
      await page.getByRole('checkbox', { name: /Подключение соответствует направлению/ }).check();
      await page.getByRole('button', { name: 'Перейти к проведению' }).click();
      await page.getByRole('button', { name: 'Подтверждаю завершение серии' }).click();
      await nav.getByRole('button', { name: 'Параметры' }).click();
      await expect(page.getByText('Текущее испытание · только просмотр')).toBeVisible();
      await expect(page.getByText('Клапан навигации')).toBeVisible();
      await expect(page.getByRole('textbox')).toHaveCount(0);
      await nav.getByRole('button', { name: 'Текущее испытание' }).click();
      await expect(page.getByRole('heading', { name: 'Выход на рабочее давление' })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Остановить испытание' })).toBeVisible();
    });
  });
}
