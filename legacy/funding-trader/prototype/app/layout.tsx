import type { Metadata } from "next";
import "../src/styles.css";

export const metadata: Metadata = {
  title: "FUNDING / ARB · 资金费率套利",
  description: "评估资金费率套利机会、预览执行风险并管理套利持仓的桌面工作台。",
  icons: {
    icon: "/favicon.svg",
  },
  openGraph: {
    title: "FUNDING / ARB · 资金费率套利",
    description: "评估资金费率套利机会、预览执行风险并管理套利持仓的桌面工作台。",
    images: [{ url: "/og.png", width: 1680, height: 945 }],
  },
  twitter: {
    card: "summary_large_image",
    title: "FUNDING / ARB · 资金费率套利",
    description: "评估资金费率套利机会、预览执行风险并管理套利持仓的桌面工作台。",
    images: ["/og.png"],
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="zh-CN">
      <body>{children}</body>
    </html>
  );
}
