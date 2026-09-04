import {arrowPaths, elbowPath, geometryKeyFor} from './arrows.ts';
import type {ArrowRect} from './arrows.ts';

test('geometryKeyFor prefers the rollup key over the issue id', () => {
  expect(geometryKeyFor(12)).toBe('12');
  expect(geometryKeyFor(12, 'parent:5')).toBe('parent:5');
});

test('elbowPath draws a straight line when both ends sit on one row', () => {
  const from: ArrowRect = {x: 0, y: 10, width: 40, height: 20};
  const to: ArrowRect = {x: 100, y: 10, width: 40, height: 20};
  expect(elbowPath(from, to)).toBe('M 40 20 L 100 20');
});

// The mutation this guards: a helper that always emits a straight line between the two
// centers would pass the case above but draw a diagonal here, which reads as a line with no
// precise meaning. An orthogonal elbow has exactly two interior corners — three L commands.
test('elbowPath elbows, not diagonals, when the rows differ', () => {
  const from: ArrowRect = {x: 0, y: 10, width: 40, height: 20};
  const to: ArrowRect = {x: 100, y: 50, width: 40, height: 20};
  const path = elbowPath(from, to);
  expect(path).toBe('M 40 20 L 70 20 L 70 60 L 100 60');
  expect(path.split('L')).toHaveLength(4);
});

test('elbowPath steps out with a fixed handle when the successor sits to the left', () => {
  const from: ArrowRect = {x: 100, y: 10, width: 40, height: 20};
  const to: ArrowRect = {x: 0, y: 50, width: 40, height: 20};
  expect(elbowPath(from, to)).toBe('M 140 20 L 148 20 L 148 60 L 0 60');
});

test('arrowPaths resolves issue-keyed ends and reports enforced', () => {
  const geometry = new Map([
    ['1', {x: 0, y: 0, width: 40, height: 20}],
    ['2', {x: 100, y: 0, width: 40, height: 20}],
  ]);
  const paths = arrowPaths([{from_issue_id: 1, to_issue_id: 2, enforced: true}], geometry);
  expect(paths).toHaveLength(1);
  expect(paths[0]).toMatchObject({key: '1>2', fromKey: '1', toKey: '2', enforced: true});
  expect(paths[0].path).toBe('M 40 10 L 100 10');
});

test('arrowPaths prefers a rollup key over the issue id at both ends', () => {
  const geometry = new Map([['parent:1', {x: 0, y: 0, width: 40, height: 20}], ['parent:2', {x: 100, y: 0, width: 40, height: 20}]]);
  const paths = arrowPaths([{
    from_issue_id: 11, to_issue_id: 22, enforced: false, from_rollup: 'parent:1', to_rollup: 'parent:2',
  }], geometry);
  expect(paths).toEqual([{key: 'parent:1>parent:2', fromKey: 'parent:1', toKey: 'parent:2', enforced: false, path: 'M 40 10 L 100 10'}]);
});

test('arrowPaths drops an arrow with either end missing from geometry', () => {
  const geometry = new Map([['1', {x: 0, y: 0, width: 40, height: 20}]]);
  expect(arrowPaths([{from_issue_id: 1, to_issue_id: 2, enforced: true}], geometry)).toEqual([]);
});
