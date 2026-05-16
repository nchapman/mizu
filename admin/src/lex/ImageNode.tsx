/* eslint-disable react-refresh/only-export-components */
// ImageNode — minimal Lexical DecoratorNode that renders an <img> in the
// rich editor. Paired with the IMAGE markdown transformer below so the
// node roundtrips as `![alt](url)` through @lexical/markdown.
//
// We don't bother with resizing, captions, or selection handles — the
// composer only needs uploaded images to appear at the caret and survive
// a round-trip to Markdown.

import {
  $applyNodeReplacement,
  DecoratorNode,
  type DOMExportOutput,
  type EditorConfig,
  type LexicalEditor,
  type LexicalNode,
  type NodeKey,
  type SerializedLexicalNode,
  type Spread,
} from "lexical";
import type { TextMatchTransformer } from "@lexical/markdown";
import { useEffect, useRef, useState, type ReactNode } from "react";

export type SerializedImageNode = Spread<
  { src: string; altText: string },
  SerializedLexicalNode
>;

export class ImageNode extends DecoratorNode<ReactNode> {
  __src: string;
  __altText: string;

  static getType(): string {
    return "image";
  }

  static clone(node: ImageNode): ImageNode {
    return new ImageNode(node.__src, node.__altText, node.__key);
  }

  constructor(src: string, altText: string, key?: NodeKey) {
    super(key);
    this.__src = src;
    this.__altText = altText;
  }

  static importJSON(json: SerializedImageNode): ImageNode {
    return $createImageNode(json.src, json.altText);
  }

  exportJSON(): SerializedImageNode {
    return {
      type: "image",
      version: 1,
      src: this.__src,
      altText: this.__altText,
    };
  }

  exportDOM(): DOMExportOutput {
    const el = document.createElement("img");
    el.setAttribute("src", this.__src);
    el.setAttribute("alt", this.__altText);
    return { element: el };
  }

  createDOM(_config: EditorConfig): HTMLElement {
    const span = document.createElement("span");
    span.className = "lex-image-wrap";
    return span;
  }

  updateDOM(): false {
    return false;
  }

  decorate(_editor: LexicalEditor, _config: EditorConfig): ReactNode {
    return <DecoratedImage src={this.__src} altText={this.__altText} />;
  }

  isInline(): boolean {
    return true;
  }
}

// Uploads return a /media/<name> URL backed by the render pipeline's
// ImageVariantStage — the file may take a fraction of a second to bake
// after Save() responds. The first <img> request can therefore 404.
// Retry a few times with a cache-busting query so the image appears
// once the bake catches up, instead of leaving a broken icon.
const MAX_RETRIES = 6;
const RETRY_DELAY_MS = 400;

function DecoratedImage({ src, altText }: { src: string; altText: string }) {
  const [attempt, setAttempt] = useState(0);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Cancel any pending retry if this decorator unmounts (e.g. user
  // deletes the image node before the bake catches up). Otherwise the
  // setTimeout fires against an unmounted component.
  useEffect(
    () => () => {
      if (timerRef.current) clearTimeout(timerRef.current);
    },
    [],
  );
  const url = attempt === 0 ? src : `${src}${src.includes("?") ? "&" : "?"}r=${attempt}`;
  return (
    <img
      src={url}
      alt={altText}
      className="lex-image"
      onError={() => {
        if (attempt >= MAX_RETRIES) return;
        timerRef.current = setTimeout(() => setAttempt((a) => a + 1), RETRY_DELAY_MS);
      }}
    />
  );
}

export function $createImageNode(src: string, altText: string): ImageNode {
  return $applyNodeReplacement(new ImageNode(src, altText));
}

export function $isImageNode(
  node: LexicalNode | null | undefined,
): node is ImageNode {
  return node instanceof ImageNode;
}

// Markdown transformer: parses `![alt](url)` typed/pasted as text into
// an ImageNode, and serializes ImageNodes back to that markdown form.
// `trigger: ")"` means the regex is evaluated as the user types the
// closing paren; for hydration from initialValue, $convertFromMarkdownString
// applies the same regex line-by-line.
export const IMAGE_TRANSFORMER: TextMatchTransformer = {
  dependencies: [ImageNode],
  export: (node) => {
    if (!$isImageNode(node)) return null;
    return `![${node.__altText}](${node.__src})`;
  },
  importRegExp: /!\[([^\]]*)\]\(([^)\s]+)\)/,
  regExp: /!\[([^\]]*)\]\(([^)\s]+)\)$/,
  replace: (textNode, match) => {
    const [, altText, src] = match;
    const img = $createImageNode(src, altText);
    textNode.replace(img);
  },
  trigger: ")",
  type: "text-match",
};
