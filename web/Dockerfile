FROM node:22.13.1-alpine@sha256:e2b39f7b64281324929257d0f8004fb6cb4bf0fdfb9aa8cedb235a766aec31da AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.29.3-alpine@sha256:5aea7cc516b419e3526f47dd1531be31a56a046cfe44754d94f9383e13e2ee99
COPY --from=build /src/dist/ /usr/share/nginx/html/
COPY nginx.conf /etc/nginx/conf.d/default.conf
USER 101:101
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -q -O /dev/null http://127.0.0.1:8080/health/ready || exit 1
