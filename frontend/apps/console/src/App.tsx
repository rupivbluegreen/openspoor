import { Suspense } from 'react';
import { createBrowserRouter, Navigate, Outlet, RouterProvider, useLocation } from 'react-router';
import { Flex, Spin } from 'antd';
import { SessionProvider, useSession } from './lib/session';
import { NAV } from './nav';
import { Shell } from './layout/Shell';
import Login from './pages/Login';
import NotFound from './pages/NotFound';

function Loading() {
  return (
    <Flex align="center" justify="center" style={{ minHeight: 240 }}>
      <Spin size="large" />
    </Flex>
  );
}

function RequireAuth() {
  const { token } = useSession();
  const loc = useLocation();
  if (!token) return <Navigate to={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`} replace />;
  return (
    <Shell>
      <Suspense fallback={<Loading />}>
        <Outlet />
      </Suspense>
    </Shell>
  );
}

function Root() {
  return (
    <SessionProvider>
      <Outlet />
    </SessionProvider>
  );
}

const pageRoutes = NAV.flatMap((g) =>
  g.items.flatMap((i) => [
    { path: i.path, Component: i.page },
    ...(i.children ?? []).map((c) => ({ path: c.path, Component: c.page })),
  ]),
);

const router = createBrowserRouter([
  {
    Component: Root,
    children: [
      { path: '/login', Component: Login },
      {
        Component: RequireAuth,
        children: [{ path: '/', element: <Navigate to="/trace/dashboard" replace /> }, ...pageRoutes, { path: '*', Component: NotFound }],
      },
    ],
  },
]);

export function App() {
  return <RouterProvider router={router} />;
}
