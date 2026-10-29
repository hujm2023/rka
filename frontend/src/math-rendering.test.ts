import 'fake-indexeddb/auto';
import {readFileSync} from 'node:fs';
import {createRequire} from 'node:module';
import {createElement} from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {expect,it} from 'vitest';
import {Markdown} from './ui';

it('uses one KaTeX engine for Markdown, Mermaid and the shipped stylesheet',()=>{
  const require=createRequire(import.meta.url);
  const engine=require.resolve('katex');
  for(const consumer of ['rehype-katex','micromark-extension-math','mermaid']){
    expect(createRequire(require.resolve(consumer)).resolve('katex')).toBe(engine);
  }
  const html=renderToStaticMarkup(createElement(Markdown,{children:'Inline $a+b$\n\n$$\n\\frac{a+b}{c+d}\n$$'}));
  const css=readFileSync(require.resolve('katex/dist/katex.min.css'),'utf8');
  expect(html).toContain('katex-display');
  expect(html).toContain('katex-base');
  expect(css).toContain('.katex-base');
  expect(html).not.toContain('katex-error');
});

it('does not turn untrusted TeX links into executable links',()=>{
  const html=renderToStaticMarkup(createElement(Markdown,{children:'$\\href{javascript:alert(1)}{click}$'}));
  expect(html).not.toMatch(/href=["']javascript:/i);
  expect(html).not.toContain('<script');
});
